package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	superscalar "github.com/parable-work/superscalar/go"
	"golang.org/x/crypto/argon2"
)

// The sizes of the hashes this package writes. A hash another runtime
// wrote with other sizes still verifies, and is written again at login.
const (
	SaltBytes = 16
	KeyBytes  = 32
)

// PasswordScalar is the catalog scalar every password is: 8 to 128
// characters (Unicode code points), with no composition rule.
const PasswordScalar = "Auth.Password"

// ErrMalformedHash is a stored password hash this package cannot read: not
// an argon2id PHC string of version 19 with a cost argon2 takes.
var ErrMalformedHash = errors.New("identity: the password hash is not an argon2id PHC string")

// Argon2Params is an argon2id cost: memory in KiB, passes and lanes.
type Argon2Params struct {
	MemoryKiB   uint32 `json:"memoryKiB"`
	Iterations  uint32 `json:"iterations"`
	Parallelism uint32 `json:"parallelism"`
}

// CheckPassword refuses a password outside Auth.Password's rule, through
// superscalar's binding, so every server and SDK checks one rule. The
// error is the scalar's message.
func CheckPassword(password string) error {
	return superscalar.Validate(PasswordScalar, password)
}

// HashPassword hashes password with argon2id at params and a fresh 16-byte
// salt, and writes the PHC string:
//
//	$argon2id$v=19$m=<memoryKiB>,t=<iterations>,p=<parallelism>$<salt>$<hash>
//
// with the salt and the 32-byte hash in standard base64 without padding.
// The password is hashed as its UTF-8 bytes, with no normalization.
func HashPassword(password string, params Argon2Params) (string, error) {
	salt := make([]byte, SaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("identity: read a salt: %w", err)
	}
	return HashPasswordWithSalt(password, salt, params), nil
}

// HashPasswordWithSalt is HashPassword with the caller's salt. It is
// deterministic, for the parity vectors; a login never reuses a salt.
func HashPasswordWithSalt(password string, salt []byte, params Argon2Params) string {
	key := argon2.IDKey([]byte(password), salt, params.Iterations, params.MemoryKiB, uint8(params.Parallelism), KeyBytes)
	return encodePHC(params, salt, key)
}

// VerifyPassword reports whether password matches the PHC string phc,
// comparing in constant time, and whether the hash should be written again
// at current's cost: when its memory, iterations or parallelism differ
// from current's, or its salt or hash are not the sizes this package
// writes. A string this package cannot read is ErrMalformedHash, and
// matches nothing.
func VerifyPassword(phc, password string, current Argon2Params) (ok, rehash bool, err error) {
	h, err := parsePHC(phc)
	if err != nil {
		return false, false, err
	}
	key := argon2.IDKey([]byte(password), h.salt, h.params.Iterations, h.params.MemoryKiB, uint8(h.params.Parallelism), uint32(len(h.key)))
	if subtle.ConstantTimeCompare(key, h.key) != 1 {
		return false, false, nil
	}
	rehash = h.params != current || len(h.salt) != SaltBytes || len(h.key) != KeyBytes
	return true, rehash, nil
}

// phcHash is a parsed argon2id PHC string.
type phcHash struct {
	params    Argon2Params
	salt, key []byte
}

func encodePHC(params Argon2Params, salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version,
		params.MemoryKiB, params.Iterations, params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// parsePHC reads an argon2id PHC string in the one form encodePHC writes:
// the version 19, the parameters m, t and p in that order as decimal
// integers without leading zeros, and the salt (8 bytes or more) and hash
// (4 bytes or more) in standard base64 without padding. A cost argon2
// does not take, or above the config's bounds, is refused, so a stored
// hash cannot make a login run without end.
func parsePHC(phc string) (phcHash, error) {
	fields := strings.Split(phc, "$")
	if len(fields) != 6 || fields[0] != "" || fields[1] != "argon2id" || fields[2] != "v="+strconv.Itoa(argon2.Version) {
		return phcHash{}, ErrMalformedHash
	}
	params := strings.Split(fields[3], ",")
	if len(params) != 3 {
		return phcHash{}, ErrMalformedHash
	}
	var p Argon2Params
	for i, dst := range []*uint32{&p.MemoryKiB, &p.Iterations, &p.Parallelism} {
		name, value, ok := strings.Cut(params[i], "=")
		if !ok || name != "mtp"[i:i+1] || !isDecimal(value) {
			return phcHash{}, ErrMalformedHash
		}
		n, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return phcHash{}, ErrMalformedHash
		}
		*dst = uint32(n)
	}
	if p.Iterations < 1 || p.Parallelism < 1 || p.Parallelism > 255 || p.MemoryKiB < 8*p.Parallelism || p.MemoryKiB > maxArgon2MemoryKiB {
		return phcHash{}, ErrMalformedHash
	}
	// The decoder skips CR and LF even in strict mode, so a salt or hash
	// with a line break would read; the PHC format has none.
	if !isBase64Text(fields[4]) || !isBase64Text(fields[5]) {
		return phcHash{}, ErrMalformedHash
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(fields[4])
	if err != nil || len(salt) < 8 {
		return phcHash{}, ErrMalformedHash
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(fields[5])
	if err != nil || len(key) < 4 {
		return phcHash{}, ErrMalformedHash
	}
	return phcHash{params: p, salt: salt, key: key}, nil
}

// isBase64Text reports whether s holds only standard base64's alphabet,
// without padding.
func isBase64Text(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '+' || c == '/') {
			return false
		}
	}
	return true
}

// isDecimal reports whether s is a decimal integer without a sign or a
// leading zero.
func isDecimal(s string) bool {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// dummyHash is a hash of a random password at params, which a login for an
// unknown account verifies against, so its timing does not tell it from a
// wrong password.
func dummyHash(params Argon2Params) (string, error) {
	password := make([]byte, 32)
	if _, err := rand.Read(password); err != nil {
		return "", fmt.Errorf("identity: read a dummy password: %w", err)
	}
	return HashPassword(base64.RawStdEncoding.EncodeToString(password), params)
}
