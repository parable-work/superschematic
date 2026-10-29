// Package fieldcrypt seals and opens the values of a DB table's
// Encrypted<T> fields (D85), which the generated Go ORM stores and reads
// through it, as the TypeScript repositories do through the HTTP runtime's
// fieldcrypt.ts, byte for byte.
//
// A value is sealed with AES-256-GCM under the keyring's current key, with
// a random 12-byte nonce, and stored as its envelope:
//
//	v1:<key id>:<nonce>:<ciphertext and tag>
//
// the nonce and the ciphertext in unpadded base64url. The additional data
// binds the envelope to its cell: the table type, the field, and the row's
// key, so a ciphertext copied to another row or column does not open.
//
// A keyring is the text of the DB service's key secret: one or more keys,
// each 32 bytes in base64, separated by commas. The first seals; every one
// opens the envelopes that name its id, the first 8 hex digits of a
// SHA-256 over the key, so a rotation puts the new key first and keeps the
// old ones until the ORM's Reencrypt has resealed every row.
package fieldcrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Version is the envelope's version, its first part.
const Version = "v1"

// KeySize is the length of a key in bytes, AES-256's.
const KeySize = 32

// NonceSize is the length of a nonce in bytes, GCM's standard one.
const NonceSize = 12

// The errors an operation fails with, which Error wraps: errors.Is tells
// them apart. None carries a value or a key.
var (
	// ErrNoKeys is a seal or an open without a keyring: the ORM was opened
	// without the DB service's field keys.
	ErrNoKeys = errors.New("no field keys: open the ORM with the DB service's key secret")
	// ErrBadKeys is a key secret that is not one or more 32-byte keys in
	// base64, separated by commas.
	ErrBadKeys = errors.New("the field keys are not one or more 32-byte keys in base64, separated by commas")
	// ErrUnknownKey is an envelope sealed under a key the keyring does not
	// hold: a wrong key secret, or an old key rotated out too soon.
	ErrUnknownKey = errors.New("no field key has the envelope's key id")
	// ErrDecrypt is an envelope that does not open under its key for its
	// cell: changed, or copied from another row or column.
	ErrDecrypt = errors.New("the envelope does not open for its row and column")
	// ErrMalformed is a value that is no envelope.
	ErrMalformed = errors.New("the value is no field envelope")
)

// Error is an operation's failure on one cell: the table type and field,
// and one of the errors above.
type Error struct {
	Table string
	Field string
	Err   error
}

func (e *Error) Error() string {
	if e.Table == "" {
		return "fieldcrypt: " + e.Err.Error()
	}
	return fmt.Sprintf("fieldcrypt: %s.%s: %s", e.Table, e.Field, e.Err.Error())
}

func (e *Error) Unwrap() error { return e.Err }

// Cell is what an envelope is bound to: the table type and the field, as
// the schema names them, and the row's key as text (a UUID hyphenated, in
// lower case). A @table or @column override, which renames them in SQL
// alone, leaves every envelope readable.
type Cell struct {
	Table string
	Field string
	Row   string
}

func (c Cell) additionalData() []byte {
	return []byte("superschematic field " + Version + "\x00" + c.Table + "\x00" + c.Field + "\x00" + c.Row)
}

type key struct {
	id   string
	aead cipher.AEAD
}

// Keyring holds the keys a DB service's key secret names: the first
// seals, and each opens the envelopes sealed under it. A nil Keyring
// fails every operation with ErrNoKeys.
type Keyring struct {
	current *key
	keys    map[string]*key
}

// KeyID is the id of a 32-byte key: the first 8 hex digits of a SHA-256
// over it.
func KeyID(raw []byte) string {
	sum := sha256.Sum256(append([]byte("superschematic field key\x00"), raw...))
	return hex.EncodeToString(sum[:4])
}

// ParseKeys reads a key secret: one or more 32-byte keys in base64, with or
// without padding, separated by commas, the first the current one.
func ParseKeys(secret string) (*Keyring, error) {
	ring := &Keyring{keys: map[string]*key{}}
	for i, part := range strings.Split(secret, ",") {
		text := strings.TrimSpace(part)
		raw, err := decodeKey(text)
		if err != nil {
			return nil, &Error{Err: fmt.Errorf("%w: key %d %s", ErrBadKeys, i+1, err.Error())}
		}
		block, err := aes.NewCipher(raw)
		if err != nil {
			return nil, &Error{Err: fmt.Errorf("%w: key %d: %s", ErrBadKeys, i+1, err.Error())}
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, &Error{Err: fmt.Errorf("%w: key %d: %s", ErrBadKeys, i+1, err.Error())}
		}
		k := &key{id: KeyID(raw), aead: aead}
		if ring.keys[k.id] != nil {
			return nil, &Error{Err: fmt.Errorf("%w: key %d repeats the key id %s", ErrBadKeys, i+1, k.id)}
		}
		ring.keys[k.id] = k
		if ring.current == nil {
			ring.current = k
		}
	}
	return ring, nil
}

func decodeKey(text string) ([]byte, error) {
	if text == "" {
		return nil, errors.New("is empty")
	}
	encoding := base64.StdEncoding
	if !strings.HasSuffix(text, "=") {
		encoding = base64.RawStdEncoding
	}
	raw, err := encoding.Strict().DecodeString(text)
	if err != nil {
		return nil, errors.New("is not base64")
	}
	if len(raw) != KeySize {
		return nil, fmt.Errorf("is %d bytes, not %d", len(raw), KeySize)
	}
	return raw, nil
}

// CurrentID is the id of the key that seals.
func (k *Keyring) CurrentID() string {
	if k == nil {
		return ""
	}
	return k.current.id
}

// CurrentPrefix is the start of every envelope the current key seals, as a
// LIKE pattern: the rows whose column does not match it hold an envelope
// of another key, which Reencrypt reseals.
func (k *Keyring) CurrentPrefix() string {
	return Version + ":" + k.CurrentID() + ":%"
}

// Seal encrypts plaintext for cell under the current key.
func (k *Keyring) Seal(cell Cell, plaintext string) (string, error) {
	nonce := make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", &Error{Table: cell.Table, Field: cell.Field, Err: err}
	}
	return k.seal(cell, plaintext, nonce)
}

func (k *Keyring) seal(cell Cell, plaintext string, nonce []byte) (string, error) {
	if k == nil {
		return "", &Error{Table: cell.Table, Field: cell.Field, Err: ErrNoKeys}
	}
	sealed := k.current.aead.Seal(nil, nonce, []byte(plaintext), cell.additionalData())
	return Version + ":" + k.current.id + ":" + base64.RawURLEncoding.EncodeToString(nonce) + ":" + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Open decrypts the envelope of cell, under the key its id names.
func (k *Keyring) Open(cell Cell, envelope string) (string, error) {
	fail := func(err error) (string, error) {
		return "", &Error{Table: cell.Table, Field: cell.Field, Err: err}
	}
	if k == nil {
		return fail(ErrNoKeys)
	}
	parts := strings.Split(envelope, ":")
	if len(parts) != 4 || parts[0] != Version {
		return fail(ErrMalformed)
	}
	nonce, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || len(nonce) != NonceSize {
		return fail(ErrMalformed)
	}
	sealed, err := base64.RawURLEncoding.Strict().DecodeString(parts[3])
	if err != nil {
		return fail(ErrMalformed)
	}
	used := k.keys[parts[1]]
	if used == nil {
		return fail(ErrUnknownKey)
	}
	plain, err := used.aead.Open(nil, nonce, sealed, cell.additionalData())
	if err != nil {
		return fail(ErrDecrypt)
	}
	return string(plain), nil
}

// Current reports whether envelope was sealed under the current key, so a
// Reencrypt leaves it as it is.
func (k *Keyring) Current(envelope string) bool {
	return k != nil && strings.HasPrefix(envelope, Version+":"+k.current.id+":")
}

// NewRowKey is a random version 4 UUID, which the ORM gives a row of a
// table with an encrypted field when its create leaves the key unset: the
// envelope is bound to the key, so the ORM knows it before the insert.
func NewRowKey() [16]byte {
	var u [16]byte
	if _, err := rand.Read(u[:]); err != nil {
		panic("fieldcrypt: no randomness: " + err.Error())
	}
	u[6] = (u[6] & 0x0f) | 0x40
	u[8] = (u[8] & 0x3f) | 0x80
	return u
}
