package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// TokenBytes is the size of a session token's random value. Its text is
// TokenLength characters of base64url without padding.
const (
	TokenBytes  = 32
	TokenLength = 43
)

// Transport is how a session travels: the Authorization header or the
// cookie. It is also the value of LoginInput's session member.
type Transport string

const (
	TransportBearer Transport = "bearer"
	TransportCookie Transport = "cookie"
)

// NewToken returns a new session token: TokenBytes random bytes from r
// (crypto/rand when nil), in base64url without padding.
func NewToken(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	b := make([]byte, TokenBytes)
	if _, err := io.ReadFull(r, b); err != nil {
		return "", fmt.Errorf("identity: read a token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken is the hash a session's row keeps of its token: the lowercase
// hexadecimal SHA-256 of the token's text, a Crypto.SHA256.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// isToken reports whether s has a token's shape: TokenLength characters of
// the base64url alphabet.
func isToken(s string) bool {
	if len(s) != TokenLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// Credential is the session token a request carries, and how it carries
// it.
type Credential struct {
	Token     string
	Transport Transport
}

// CredentialOutcome is what ExtractCredential found on a request.
type CredentialOutcome int

const (
	// NoCredential: no Authorization header and no session cookie.
	NoCredential CredentialOutcome = iota
	// UsableCredential: a token in the Authorization header or the cookie.
	UsableCredential
	// InvalidCredential: an Authorization header that is not a usable
	// bearer token, or, without one, a session cookie whose value is not a
	// token. It is 401, with no fallback to the cookie.
	InvalidCredential
)

// ExtractCredential reads a request's session credential. Authorization
// comes first: when the request has one it is the credential, and the
// cookie is not read. It is usable when it is "Bearer", in any case,
// then one or more spaces and a token (TokenLength characters of
// base64url); anything else is InvalidCredential. Without Authorization
// the first cookie named cookieName is the credential, usable when its
// value is a token. The returned credential's Transport says which was
// read, InvalidCredential included; it is empty with NoCredential.
func ExtractCredential(h http.Header, cookieName string) (Credential, CredentialOutcome) {
	if values, present := h["Authorization"]; present {
		token, ok := bearerToken(values[0])
		if !ok || len(values) != 1 {
			return Credential{Transport: TransportBearer}, InvalidCredential
		}
		return Credential{Token: token, Transport: TransportBearer}, UsableCredential
	}
	r := http.Request{Header: h}
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return Credential{}, NoCredential
	}
	if !isToken(cookie.Value) {
		return Credential{Transport: TransportCookie}, InvalidCredential
	}
	return Credential{Token: cookie.Value, Transport: TransportCookie}, UsableCredential
}

// bearerToken reads "Bearer <token>": the scheme in any case, one or more
// spaces, and a token.
func bearerToken(v string) (string, bool) {
	scheme, rest, ok := strings.Cut(v, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token := strings.TrimLeft(rest, " ")
	if !isToken(token) {
		return "", false
	}
	return token, true
}
