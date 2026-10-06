package local

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// KeysDir is the directory, in a local environment's state directory, that
// holds each http edge's key pair: `<key pair node>.jwk`, the private JWK,
// readable by its owner alone. The output root never holds one.
const KeysDir = "keys"

// JWK is a JSON Web Key (RFC 7517) of an Ed25519 key, as the HTTP runtimes'
// serviceauth package reads it: an OKP key with its RFC 7638 thumbprint as
// kid, and the private member d only in the caller's copy.
type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	Kid string `json:"kid"`
	Alg string `json:"alg,omitempty"`
	Use string `json:"use,omitempty"`
	X   string `json:"x"`
	D   string `json:"d,omitempty"`
}

// Public returns the key without its private member.
func (k JWK) Public() JWK {
	k.D = ""
	return k
}

// NewKeyPair generates an Ed25519 key pair from random and returns its
// private JWK.
func NewKeyPair(random io.Reader) (JWK, error) {
	public, private, err := ed25519.GenerateKey(random)
	if err != nil {
		return JWK{}, err
	}
	x := base64.RawURLEncoding.EncodeToString(public)
	return JWK{
		Kty: "OKP",
		Crv: KeyAlgorithm,
		Kid: Thumbprint(x),
		Alg: TokenAlgorithm,
		Use: "sig",
		X:   x,
		D:   base64.RawURLEncoding.EncodeToString(private.Seed()),
	}, nil
}

// Thumbprint is the RFC 7638 thumbprint of an Ed25519 public key, given as
// its base64url x: the base64url SHA-256 of {"crv","kty","x"} in that
// order, with no whitespace.
func Thumbprint(x string) string {
	sum := sha256.Sum256([]byte(`{"crv":"` + KeyAlgorithm + `","kty":"OKP","x":"` + x + `"}`))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// keyPath is where a key pair's private JWK is kept.
func keyPath(stateDir, id string) string {
	return filepath.Join(stateDir, KeysDir, id+".jwk")
}

// readKey reads a key pair's private JWK, and checks that its public key is
// its private key's.
func readKey(stateDir, id string) (JWK, error) {
	path := keyPath(stateDir, id)
	data, err := os.ReadFile(path)
	if err != nil {
		return JWK{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var k JWK
	if err := decoder.Decode(&k); err != nil {
		return JWK{}, fmt.Errorf("local: key %s: %w", path, err)
	}
	seed, err := base64.RawURLEncoding.DecodeString(k.D)
	if err != nil || len(seed) != ed25519.SeedSize || k.Kty != "OKP" || k.Crv != KeyAlgorithm {
		return JWK{}, fmt.Errorf("local: key %s is not a private Ed25519 JWK", path)
	}
	public := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if k.X != base64.RawURLEncoding.EncodeToString(public) || k.Kid != Thumbprint(k.X) {
		return JWK{}, fmt.Errorf("local: key %s: its x or kid is not its private key's", path)
	}
	return k, nil
}

// ensureKey reads a key pair, or generates it and writes it, readable by
// its owner alone, when it is missing. It reports whether it generated one.
func ensureKey(stateDir, id string, random io.Reader) (JWK, bool, error) {
	k, err := readKey(stateDir, id)
	if err == nil {
		return k, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return JWK{}, false, err
	}
	if random == nil {
		random = rand.Reader
	}
	if k, err = NewKeyPair(random); err != nil {
		return JWK{}, false, fmt.Errorf("local: key %s: %w", id, err)
	}
	data, err := json.Marshal(k)
	if err != nil {
		return JWK{}, false, err
	}
	path := keyPath(stateDir, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return JWK{}, false, fmt.Errorf("local: key %s: %w", id, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return JWK{}, false, fmt.Errorf("local: key %s: %w", id, err)
	}
	return k, true, nil
}
