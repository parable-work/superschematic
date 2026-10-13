package serviceauth

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// The header algs a config may list. Ed25519 is RFC 9864's name for EdDSA
// over Ed25519; a token header carrying it is read as EdDSA.
const (
	AlgRS256   = "RS256"
	AlgES256   = "ES256"
	AlgEdDSA   = "EdDSA"
	AlgEd25519 = "Ed25519"
)

// minRSABits is the smallest RSA modulus accepted, as RFC 7518 requires for
// RS256.
const minRSABits = 2048

// publicKey is a parsed public JWK of one of the supported types.
type publicKey struct {
	rsa *rsa.PublicKey
	ec  *ecdsa.PublicKey
	ed  ed25519.PublicKey
}

// fits reports whether the key's type is the one alg signs with: RS256 an
// RSA key, ES256 a P-256 key, EdDSA an Ed25519 key. It is what stops an alg
// confusion, such as an RS256 key read as an ES256 or an HMAC secret.
func (k publicKey) fits(alg string) bool {
	switch alg {
	case AlgRS256:
		return k.rsa != nil
	case AlgES256:
		return k.ec != nil
	case AlgEdDSA:
		return k.ed != nil
	}
	return false
}

// verify checks sig over input under alg. The caller has checked fits.
func (k publicKey) verify(alg string, input, sig []byte) bool {
	switch alg {
	case AlgRS256:
		digest := sha256.Sum256(input)
		return rsa.VerifyPKCS1v15(k.rsa, crypto.SHA256, digest[:], sig) == nil
	case AlgES256:
		// A JWS ES256 signature is r||s, 32 bytes each (RFC 7518 section
		// 3.4), not the ASN.1 form ecdsa.VerifyASN1 reads.
		if len(sig) != 64 {
			return false
		}
		digest := sha256.Sum256(input)
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		return ecdsa.Verify(k.ec, digest[:], r, s)
	case AlgEdDSA:
		return len(sig) == ed25519.SignatureSize && ed25519.Verify(k.ed, input, sig)
	}
	return false
}

// parsePublicJWK reads a public JWK of a supported type. A key carrying the
// private member d is refused, since it does not belong in a callee's
// config or a published key set.
func parsePublicJWK(j JWK) (publicKey, error) {
	if j.D != "" {
		return publicKey{}, errors.New("key carries a private member (d)")
	}
	switch j.Kty {
	case "RSA":
		n, err := jwkBytes("n", j.N)
		if err != nil {
			return publicKey{}, err
		}
		e, err := jwkBytes("e", j.E)
		if err != nil {
			return publicKey{}, err
		}
		modulus := new(big.Int).SetBytes(n)
		if modulus.BitLen() < minRSABits {
			return publicKey{}, fmt.Errorf("RSA modulus has %d bits, fewer than %d", modulus.BitLen(), minRSABits)
		}
		exponent := new(big.Int).SetBytes(e)
		if !exponent.IsInt64() || exponent.Int64() < 3 || exponent.Int64() > 1<<31-1 || exponent.Bit(0) == 0 {
			return publicKey{}, errors.New("RSA exponent is not an odd integer from 3 to 2^31-1")
		}
		return publicKey{rsa: &rsa.PublicKey{N: modulus, E: int(exponent.Int64())}}, nil
	case "EC":
		if j.Crv != "P-256" {
			return publicKey{}, fmt.Errorf("EC curve %q is not P-256", j.Crv)
		}
		x, err := jwkBytes("x", j.X)
		if err != nil {
			return publicKey{}, err
		}
		y, err := jwkBytes("y", j.Y)
		if err != nil {
			return publicKey{}, err
		}
		if len(x) != 32 || len(y) != 32 {
			return publicKey{}, errors.New("P-256 coordinates are not 32 bytes each")
		}
		point := append(append([]byte{4}, x...), y...)
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
		if err != nil {
			return publicKey{}, fmt.Errorf("P-256 point: %w", err)
		}
		return publicKey{ec: pub}, nil
	case "OKP":
		if j.Crv != "Ed25519" {
			return publicKey{}, fmt.Errorf("OKP curve %q is not Ed25519", j.Crv)
		}
		x, err := jwkBytes("x", j.X)
		if err != nil {
			return publicKey{}, err
		}
		if len(x) != ed25519.PublicKeySize {
			return publicKey{}, errors.New("Ed25519 key is not 32 bytes")
		}
		return publicKey{ed: ed25519.PublicKey(x)}, nil
	case "":
		return publicKey{}, errors.New("key has no kty")
	}
	return publicKey{}, fmt.Errorf("key type %q is not supported", j.Kty)
}

// jwkBytes decodes a base64url JWK member. Padding, which RFC 7518 leaves
// out, is tolerated in a key.
func jwkBytes(name, value string) ([]byte, error) {
	if value == "" {
		return nil, fmt.Errorf("key has no %s", name)
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(value, "="))
	if err != nil {
		return nil, fmt.Errorf("key member %s is not base64url: %w", name, err)
	}
	return b, nil
}

// The DER of an Ed25519 key up to the key's own 32 bytes (RFC 8410): the
// SubjectPublicKeyInfo of a public key, whose bytes are the key, and the
// PKCS#8 (version 1) PrivateKeyInfo of a private one, whose bytes are its
// seed. The tls provider writes both, as Go's x509 package and OpenSSL do;
// no other encoding is read.
var (
	ed25519SPKIPrefix  = []byte{0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x03, 0x21, 0x00}
	ed25519PKCS8Prefix = []byte{0x30, 0x2e, 0x02, 0x01, 0x00, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x04, 0x22, 0x04, 0x20}
)

// IsPEM reports whether a key's text is a PEM rather than a JWK's JSON:
// it begins, after whitespace, with "-----BEGIN ".
func IsPEM(text []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(text, " \t\r\n"), []byte("-----BEGIN "))
}

// pemContent returns the DER one PEM block labeled label holds: text,
// without the whitespace around it, is "-----BEGIN <label>-----", the
// base64 of the DER, padded, in lines of any length, and "-----END
// <label>-----". The TypeScript and Rust runtimes read a PEM by the same
// rule.
func pemContent(text []byte, label string) ([]byte, error) {
	begin, end := "-----BEGIN "+label+"-----", "-----END "+label+"-----"
	trimmed := strings.Trim(string(text), " \t\r\n")
	if !strings.HasPrefix(trimmed, begin) || !strings.HasSuffix(trimmed, end) || len(trimmed) < len(begin)+len(end) {
		return nil, fmt.Errorf("no PEM %s block", label)
	}
	body := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, trimmed[len(begin):len(trimmed)-len(end)])
	der, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("the PEM %s block is not base64", label)
	}
	return der, nil
}

// PublicJWKFromPEM reads an Ed25519 public key's PEM, the
// SubjectPublicKeyInfo `-----BEGIN PUBLIC KEY-----` (RFC 8410) that the
// generic connector's key node writes into a callee's callers field (D63),
// as the public JWK a Config's Keys hold: an OKP Ed25519 key whose kid is
// its RFC 7638 thumbprint, the kid SignedToken writes for the key's
// private half. Any other key, such as an RSA or a P-256 one, is refused.
func PublicJWKFromPEM(text []byte) (JWK, error) {
	der, err := pemContent(text, "PUBLIC KEY")
	if err != nil {
		return JWK{}, err
	}
	if len(der) != len(ed25519SPKIPrefix)+ed25519.PublicKeySize || !bytes.HasPrefix(der, ed25519SPKIPrefix) {
		return JWK{}, errors.New("not an Ed25519 public key's SubjectPublicKeyInfo")
	}
	return ed25519JWK(der[len(ed25519SPKIPrefix):])
}

// privateKeyFromPEM reads an Ed25519 private key's PEM, the PKCS#8
// `-----BEGIN PRIVATE KEY-----` (RFC 8410) that the generic connector's key
// node writes (D63), and returns the key with its public JWK, whose kid is
// its thumbprint.
func privateKeyFromPEM(text []byte) (ed25519.PrivateKey, JWK, error) {
	der, err := pemContent(text, "PRIVATE KEY")
	if err != nil {
		return nil, JWK{}, err
	}
	if len(der) != len(ed25519PKCS8Prefix)+ed25519.SeedSize || !bytes.HasPrefix(der, ed25519PKCS8Prefix) {
		return nil, JWK{}, errors.New("not an Ed25519 private key's PKCS#8")
	}
	key := ed25519.NewKeyFromSeed(der[len(ed25519PKCS8Prefix):])
	public, err := ed25519JWK(key.Public().(ed25519.PublicKey))
	return key, public, err
}

// ed25519JWK is the public JWK of an Ed25519 key, its kid its thumbprint.
func ed25519JWK(public []byte) (JWK, error) {
	j := JWK{Kty: "OKP", Crv: "Ed25519", X: base64.RawURLEncoding.EncodeToString(public)}
	kid, err := Thumbprint(j)
	j.Kid = kid
	return j, err
}

// Thumbprint returns the RFC 7638 thumbprint of a public JWK, base64url
// encoded: the SHA-256 of its required members in lexical order. The
// generic connector names an edge's key by it, in kid.
func Thumbprint(j JWK) (string, error) {
	var members []byte
	var err error
	switch j.Kty {
	case "RSA":
		members, err = json.Marshal(struct {
			E   string `json:"e"`
			Kty string `json:"kty"`
			N   string `json:"n"`
		}{j.E, j.Kty, j.N})
	case "EC":
		members, err = json.Marshal(struct {
			Crv string `json:"crv"`
			Kty string `json:"kty"`
			X   string `json:"x"`
			Y   string `json:"y"`
		}{j.Crv, j.Kty, j.X, j.Y})
	case "OKP":
		members, err = json.Marshal(struct {
			Crv string `json:"crv"`
			Kty string `json:"kty"`
			X   string `json:"x"`
		}{j.Crv, j.Kty, j.X})
	default:
		return "", fmt.Errorf("serviceauth: no thumbprint for key type %q", j.Kty)
	}
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(members)
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}
