package serviceauth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// TokenSource returns a service credential for a client's
// Service-Authorization header. fresh asks for a token other than the one
// cached, after the callee refused it with service_unauthorized. It is the
// SDKs' ServiceCredential token function. The sources here are safe for
// concurrent use; a fetch or read runs once while concurrent callers wait.
type TokenSource func(ctx context.Context, fresh bool) (string, error)

const (
	// metadataHost is the GCE and Cloud Run metadata server, which
	// GCE_METADATA_HOST replaces.
	metadataHost = "metadata.google.internal"
	// googleRenewBefore is how long before its exp a Google ID token is
	// fetched again.
	googleRenewBefore = 5 * time.Minute
	// tokenFileMaxAge is how long a token read from a file serves before it
	// is read again; the kubelet replaces a projected token at 80% of its
	// lifetime.
	tokenFileMaxAge = 60 * time.Second
	// signedTokenLifetime is exp - iat of a SignedToken.
	signedTokenLifetime = 300
	// signedTokenRenewBefore is the time left at which SignedToken signs a
	// new token.
	signedTokenRenewBefore = 60
	// maxTokenBytes bounds a token read from the metadata server.
	maxTokenBytes = 64 << 10
)

// GoogleIDToken is the Cloud Run caller's source: a Google ID token for
// audience, the callee's URL, from the metadata server
// (GCE_METADATA_HOST when set, else metadata.google.internal). The token is
// cached until 5 minutes before its exp. It reads WithClock and
// WithHTTPClient.
func GoogleIDToken(audience string, opts ...Option) TokenSource {
	o := buildOptions(opts)
	var (
		mu     sync.Mutex
		token  string
		expiry time.Time
	)
	return func(ctx context.Context, fresh bool) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if !fresh && token != "" && o.now().Before(expiry.Add(-googleRenewBefore)) {
			return token, nil
		}
		host := os.Getenv("GCE_METADATA_HOST")
		if host == "" {
			host = metadataHost
		}
		endpoint := "http://" + host + "/computeMetadata/v1/instance/service-accounts/default/identity?audience=" + url.QueryEscape(audience)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return "", fmt.Errorf("serviceauth: google id token: %w", err)
		}
		req.Header.Set("Metadata-Flavor", "Google")
		resp, err := o.client.Do(req)
		if err != nil {
			return "", fmt.Errorf("serviceauth: google id token: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenBytes))
		if err != nil {
			return "", fmt.Errorf("serviceauth: google id token: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("serviceauth: google id token: metadata server answered %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		fetched := strings.TrimSpace(string(body))
		exp, err := tokenExpiry(fetched)
		if err != nil {
			return "", fmt.Errorf("serviceauth: google id token: %w", err)
		}
		token, expiry = fetched, exp
		return token, nil
	}
}

// tokenExpiry reads the exp claim of a JWT without verifying it.
func tokenExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, errors.New("token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("token payload: %w", err)
	}
	var claims struct {
		Exp *float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, fmt.Errorf("token payload: %w", err)
	}
	if claims.Exp == nil {
		return time.Time{}, errors.New("token has no exp")
	}
	return time.Unix(int64(*claims.Exp), 0), nil
}

// TokenFile is the Kubernetes caller's source: a projected service account
// token, read from path and trimmed, and read again when the last read is
// 60 seconds old. It reads WithClock.
func TokenFile(path string, opts ...Option) TokenSource {
	o := buildOptions(opts)
	var (
		mu     sync.Mutex
		token  string
		readAt time.Time
	)
	return func(ctx context.Context, fresh bool) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		now := o.now()
		if !fresh && token != "" && now.Sub(readAt) < tokenFileMaxAge {
			return token, nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("serviceauth: token file: %w", err)
		}
		read := strings.TrimSpace(string(data))
		if read == "" {
			return "", fmt.Errorf("serviceauth: token file %s is empty", path)
		}
		token, readAt = read, now
		return token, nil
	}
}

// SignedToken is the generic connector's and the local target's source: a
// compact JWS signed with an edge's Ed25519 private key, privateJWK
// ({"kty":"OKP","crv":"Ed25519","d":...,"x":...,"kid":...}). The header is
// {"alg":"EdDSA","kid":<kid>,"typ":"JWT"} and the claims
// {"iss","sub","aud","iat","exp","jti"} in that order, exp 300 seconds
// after iat and jti 16 random bytes, base64url. A new token is signed when
// the one held has under 60 seconds left. It reads WithClock and
// WithRandom.
func SignedToken(privateJWK []byte, issuer, subject, audience string, opts ...Option) (TokenSource, error) {
	var j JWK
	if err := json.Unmarshal(privateJWK, &j); err != nil {
		return nil, fmt.Errorf("serviceauth: signing key: %w", err)
	}
	if j.Kty != "OKP" || j.Crv != "Ed25519" {
		return nil, fmt.Errorf("serviceauth: signing key is %s %s, not OKP Ed25519", j.Kty, j.Crv)
	}
	if j.Kid == "" {
		return nil, errors.New("serviceauth: signing key has no kid")
	}
	seed, err := jwkBytes("d", j.D)
	if err != nil {
		return nil, fmt.Errorf("serviceauth: signing key: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("serviceauth: signing key d is not 32 bytes")
	}
	key := ed25519.NewKeyFromSeed(seed)
	x, err := jwkBytes("x", j.X)
	if err != nil {
		return nil, fmt.Errorf("serviceauth: signing key: %w", err)
	}
	if !bytes.Equal(x, key.Public().(ed25519.PublicKey)) {
		return nil, errors.New("serviceauth: signing key x is not the public key of d")
	}
	header, err := compactJSON(struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}{AlgEdDSA, j.Kid, "JWT"})
	if err != nil {
		return nil, err
	}
	encodedHeader := base64.RawURLEncoding.EncodeToString(header)

	o := buildOptions(opts)
	random := o.random
	if random == nil {
		random = rand.Reader
	}
	var (
		mu     sync.Mutex
		token  string
		expiry int64
	)
	return func(ctx context.Context, fresh bool) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		now := o.now().Unix()
		if !fresh && token != "" && expiry-now >= signedTokenRenewBefore {
			return token, nil
		}
		var jti [16]byte
		if _, err := io.ReadFull(random, jti[:]); err != nil {
			return "", fmt.Errorf("serviceauth: jti: %w", err)
		}
		claims, err := compactJSON(struct {
			Iss string `json:"iss"`
			Sub string `json:"sub"`
			Aud string `json:"aud"`
			Iat int64  `json:"iat"`
			Exp int64  `json:"exp"`
			Jti string `json:"jti"`
		}{issuer, subject, audience, now, now + signedTokenLifetime, base64.RawURLEncoding.EncodeToString(jti[:])})
		if err != nil {
			return "", err
		}
		input := encodedHeader + "." + base64.RawURLEncoding.EncodeToString(claims)
		signature := ed25519.Sign(key, []byte(input))
		token = input + "." + base64.RawURLEncoding.EncodeToString(signature)
		expiry = now + signedTokenLifetime
		return token, nil
	}, nil
}

// compactJSON marshals v without escaping <, > and &, as JSON.stringify and
// serde_json write it.
func compactJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
