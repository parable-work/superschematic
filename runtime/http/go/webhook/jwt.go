// Package webhook holds verifiers a generated server's WebhookVerifiers
// map takes for an @hmacVerified provider (D81). A verifier is middleware:
// it answers a request it refuses and passes one it accepts on, with its
// body readable again.
package webhook

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/parable-work/superschematic/runtime/http/go/requestctx"
	"github.com/parable-work/superschematic/runtime/http/go/response"
	"github.com/parable-work/superschematic/runtime/http/go/serviceauth"
	"go.uber.org/zap"
)

// The refusals of a JWTBodyHashVerifier, the problem every runtime's
// answers. The detail names no reason; the reason goes to the log.
const (
	// CodeInvalidSignature: no token, or one that does not verify. 401.
	CodeInvalidSignature   = "invalid_signature"
	DetailInvalidSignature = "The webhook signature does not match"
	// CodeKeyUnavailable: the provider could not be asked for the key, and
	// none is held. 503, so the provider sends the webhook again.
	CodeKeyUnavailable   = "service_unavailable"
	DetailKeyUnavailable = "The webhook's signing key could not be fetched"
	// CodePayloadTooLarge: a body over MaxBodyBytes. 413.
	CodePayloadTooLarge   = "payload_too_large"
	DetailPayloadTooLarge = "Request body exceeds the accepted size"
)

const (
	// AlgES256 is the one algorithm a JWTBodyHashVerifier accepts: ECDSA
	// over P-256 with SHA-256, whose JWS signature is r||s.
	AlgES256 = "ES256"
	// DefaultClockSkew is how far in the future an iat may be when
	// ClockSkew is zero.
	DefaultClockSkew = 60 * time.Second
	// DefaultMaxBodyBytes is the largest body read when MaxBodyBytes is
	// zero: 1 MiB, the TypeScript router's default body cap.
	DefaultMaxBodyBytes = 1 << 20
)

const (
	// keyMaxAge is how long a held key serves before a request that needs
	// it asks the provider again.
	keyMaxAge = time.Hour
	// lookupMinInterval is the least time between two lookups, whatever
	// prompts them, so a flood of unknown kids costs one lookup a minute.
	lookupMinInterval = 60 * time.Second
	// lookupTimeout bounds one lookup. It does not follow the request that
	// prompted it, which others may be waiting on.
	lookupTimeout = 10 * time.Second
)

// KeyLookup answers the provider's public key with kid, as Plaid's
// /webhook_verification_key/get does: nil and no error when the provider
// has no such key (or has expired it), an error when it cannot be asked.
type KeyLookup func(ctx context.Context, kid string) (*serviceauth.JWK, error)

// JWTBodyHashOptions configures JWTBodyHashVerifier. Header, one of Keys
// and JWKSURL, Alg, MaxAge and BodyHashClaim are required.
type JWTBodyHashOptions struct {
	// Header carries the compact JWS, such as Plaid-Verification.
	Header string
	// Keys looks a key up by kid. Set it or JWKSURL.
	Keys KeyLookup
	// JWKSURL is the provider's key set, fetched whole; its keys replace
	// every key held.
	JWKSURL string
	// Fetch fetches JWKSURL; serviceauth.HTTPFetcher by default.
	Fetch serviceauth.Fetcher
	// Alg is the algorithm the token header must name: AlgES256.
	Alg string
	// MaxAge is the oldest iat accepted.
	MaxAge time.Duration
	// BodyHashClaim names the claim holding the lowercase hex SHA-256 of
	// the raw body, such as request_body_sha256.
	BodyHashClaim string
	// ClockSkew is how far in the future an iat may be; zero is
	// DefaultClockSkew.
	ClockSkew time.Duration
	// MaxBodyBytes is the largest body read; zero is DefaultMaxBodyBytes.
	MaxBodyBytes int64
	// Now is the clock; time.Now by default.
	Now func() time.Time
}

// JWTBodyHashVerifier is the verifier of a provider that signs each
// webhook with a JWT carrying the SHA-256 of the raw body, as Plaid does.
// It runs the checks of the TypeScript (jwtBodyHashVerifier) and Rust
// (JwtBodyHashVerifier) verifiers, in their order, so a request gets the
// same answer from each:
//
//  1. Header holds a compact JWS: three base64url segments, the first two
//     JSON objects. Its alg is Alg (never none) and its kid a non-empty
//     string.
//  2. The key with that kid, held or looked up (below). One that cannot be
//     looked up, with none held: 503.
//  3. The signature verifies with the key, a P-256 key whose use, when
//     set, is sig and whose alg, when set, is Alg.
//  4. iat is a number, at most MaxAge in the past and ClockSkew in the
//     future.
//  5. The body, read up to MaxBodyBytes (413 past it), has the SHA-256
//     whose lowercase hex BodyHashClaim holds, compared in constant time.
//
// Any other refusal is 401 invalid_signature. The next handler reads the
// body after it.
//
// Keys are held by kid. A held key serves for an hour; then the next
// request that needs it looks it up again, and a lookup that fails keeps
// it. A kid not held is looked up at once, but the provider is asked at
// most once a minute, whatever prompts it. One lookup runs at a time: a
// request whose key is held does not wait for it, and one whose kid is not
// held waits, then looks again.
//
// The result is assignable to a generated server's WebhookVerifier.
func JWTBodyHashVerifier(opts JWTBodyHashOptions) (func(http.Handler) http.Handler, error) {
	v, err := newVerifier(opts)
	if err != nil {
		return nil, err
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := v.verify(r)
			if err != nil {
				refuse(w, r, err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
			next.ServeHTTP(w, r)
		})
	}, nil
}

type verifier struct {
	header       string
	alg          string
	maxAge       float64
	clockSkew    float64
	claim        string
	maxBodyBytes int64
	now          func() time.Time
	keys         *keyCache
}

func newVerifier(opts JWTBodyHashOptions) (*verifier, error) {
	fail := func(format string, args ...any) (*verifier, error) {
		return nil, fmt.Errorf("webhook.JWTBodyHashVerifier: "+format, args...)
	}
	switch {
	case strings.TrimSpace(opts.Header) == "":
		return fail("Header is required (the header that carries the token, such as Plaid-Verification)")
	case opts.Alg != AlgES256:
		return fail("Alg %q is not supported; use %s", opts.Alg, AlgES256)
	case opts.MaxAge <= 0:
		return fail("MaxAge must be positive")
	case opts.BodyHashClaim == "":
		return fail("BodyHashClaim is required (the claim that holds the body's SHA-256, such as request_body_sha256)")
	case opts.ClockSkew < 0:
		return fail("ClockSkew must not be negative")
	case opts.MaxBodyBytes < 0:
		return fail("MaxBodyBytes must not be negative")
	case (opts.Keys == nil) == (opts.JWKSURL == ""):
		return fail("set one of Keys and JWKSURL")
	}
	v := &verifier{
		header:       opts.Header,
		alg:          opts.Alg,
		maxAge:       opts.MaxAge.Seconds(),
		clockSkew:    DefaultClockSkew.Seconds(),
		claim:        opts.BodyHashClaim,
		maxBodyBytes: DefaultMaxBodyBytes,
		now:          opts.Now,
	}
	if opts.ClockSkew > 0 {
		v.clockSkew = opts.ClockSkew.Seconds()
	}
	if opts.MaxBodyBytes > 0 {
		v.maxBodyBytes = opts.MaxBodyBytes
	}
	if v.now == nil {
		v.now = time.Now
	}
	cache := &keyCache{now: v.now, held: map[string]heldKey{}}
	if opts.Keys != nil {
		cache.lookup = lookupOne(opts.Keys, opts.Alg)
	} else {
		fetch := opts.Fetch
		if fetch == nil {
			fetch = serviceauth.HTTPFetcher(nil)
		}
		cache.lookup = lookupSet(fetch, opts.JWKSURL, opts.Alg)
		cache.replaces = true
	}
	v.keys = cache
	return v, nil
}

// refusal is a verifier's answer to a request it refuses, and why.
type refusal struct {
	status int
	code   string
	detail string
	cause  error
}

func (e *refusal) Error() string { return e.code + ": " + e.cause.Error() }

func invalid(format string, args ...any) *refusal {
	return &refusal{http.StatusUnauthorized, CodeInvalidSignature, DetailInvalidSignature, fmt.Errorf(format, args...)}
}

// errorCodeRecorder is the request log middleware's response recorder, as
// serviceauth.WriteError uses it, so the span carries the problem code.
type errorCodeRecorder interface {
	SetErrorCode(code string)
}

// refuse answers a refusal with the problem body the rate limit writes, and
// logs its cause on the request's logger (Warn, or Error for a 5xx).
func refuse(w http.ResponseWriter, r *http.Request, err error) {
	var e *refusal
	if !errors.As(err, &e) {
		e = &refusal{http.StatusServiceUnavailable, CodeKeyUnavailable, DetailKeyUnavailable, err}
	}
	if rec, ok := w.(errorCodeRecorder); ok {
		rec.SetErrorCode(e.code)
	}
	fields := []zap.Field{zap.Int("status", e.status), zap.String("error_code", e.code), zap.NamedError("cause", e.cause)}
	logger := requestctx.LoggerFromContext(r.Context())
	if e.status >= http.StatusInternalServerError {
		logger.Error("webhook refused", fields...)
	} else {
		logger.Warn("webhook refused", fields...)
	}
	response.RequestError(w, r, e.status, e.detail, e.code)
}

// verify runs the checks, in order, and returns the body it read.
func (v *verifier) verify(r *http.Request) ([]byte, error) {
	values := r.Header.Values(v.header)
	if len(values) > 1 {
		return nil, invalid("two %s headers", v.header)
	}
	if len(values) == 0 || values[0] == "" {
		return nil, invalid("no %s header", v.header)
	}
	token := values[0]
	segments := strings.Split(token, ".")
	if len(segments) != 3 {
		return nil, invalid("the token is not a compact JWS")
	}
	header, okHeader := jsonSegment(segments[0])
	claims, okClaims := jsonSegment(segments[1])
	signature, errSig := base64.RawURLEncoding.DecodeString(segments[2])
	if !okHeader || !okClaims || errSig != nil {
		return nil, invalid("a segment of the token is not base64url JSON")
	}
	if alg, _ := header["alg"].(string); alg != v.alg {
		return nil, invalid("the token's alg %v is not %s", header["alg"], v.alg)
	}
	kid, _ := header["kid"].(string)
	if kid == "" {
		return nil, invalid("the token has no kid")
	}
	key, err := v.keys.key(r.Context(), kid)
	if err != nil {
		return nil, err
	}
	if !verifyES256(key, []byte(segments[0]+"."+segments[1]), signature) {
		return nil, invalid("the signature does not verify with key %q", kid)
	}
	iat, ok := claims["iat"].(float64)
	if !ok {
		return nil, invalid("the token has no numeric iat")
	}
	now := float64(v.now().UnixNano()) / 1e9
	if iat > now+v.clockSkew {
		return nil, invalid("the token's iat is in the future")
	}
	if now-iat > v.maxAge {
		return nil, invalid("the token's iat is older than the maximum age")
	}
	claimed, ok := claims[v.claim].(string)
	if !ok {
		return nil, invalid("the token has no %s claim", v.claim)
	}
	body, err := v.readBody(r)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	if subtle.ConstantTimeCompare([]byte(claimed), []byte(hex.EncodeToString(sum[:]))) != 1 {
		return nil, invalid("the body's SHA-256 is not the token's %s", v.claim)
	}
	return body, nil
}

// readBody reads the body, up to maxBodyBytes.
func (v *verifier) readBody(r *http.Request) ([]byte, error) {
	tooLarge := &refusal{http.StatusRequestEntityTooLarge, CodePayloadTooLarge, DetailPayloadTooLarge, fmt.Errorf("the body is over %d bytes", v.maxBodyBytes)}
	if r.ContentLength > v.maxBodyBytes {
		return nil, tooLarge
	}
	if r.Body == nil {
		return []byte{}, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, v.maxBodyBytes+1))
	if err != nil {
		return nil, &refusal{http.StatusBadRequest, "bad_request", "The request body could not be read", err}
	}
	if int64(len(body)) > v.maxBodyBytes {
		return nil, tooLarge
	}
	return body, nil
}

// jsonSegment decodes a base64url segment holding a JSON object.
func jsonSegment(segment string) (map[string]any, bool) {
	data, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return nil, false
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return nil, false
	}
	return object, true
}

// verifyES256 checks a JWS ES256 signature: r||s, 32 bytes each (RFC 7518
// section 3.4), not the ASN.1 form ecdsa.VerifyASN1 reads.
func verifyES256(key *ecdsa.PublicKey, input, signature []byte) bool {
	if len(signature) != 64 {
		return false
	}
	digest := sha256.Sum256(input)
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:])
	return ecdsa.Verify(key, digest[:], r, s)
}

// p256Key reads a public P-256 JWK the verifier can use: no private
// member, a use of sig and an alg of alg when they are set.
func p256Key(j serviceauth.JWK, alg string) (*ecdsa.PublicKey, error) {
	switch {
	case j.D != "":
		return nil, errors.New("the key carries a private member (d)")
	case j.Kty != "EC" || j.Crv != "P-256":
		return nil, fmt.Errorf("the key is %s %s, not EC P-256", j.Kty, j.Crv)
	case j.Use != "" && j.Use != "sig":
		return nil, fmt.Errorf("the key's use is %q, not sig", j.Use)
	case j.Alg != "" && j.Alg != alg:
		return nil, fmt.Errorf("the key's alg is %q, not %s", j.Alg, alg)
	}
	x, errX := base64.RawURLEncoding.DecodeString(strings.TrimRight(j.X, "="))
	y, errY := base64.RawURLEncoding.DecodeString(strings.TrimRight(j.Y, "="))
	if errX != nil || errY != nil || len(x) != 32 || len(y) != 32 {
		return nil, errors.New("the key's x and y are not 32 bytes of base64url each")
	}
	return ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
}

// lookupOne asks a KeyLookup for one kid. A key the verifier cannot use is
// no key.
func lookupOne(lookup KeyLookup, alg string) func(context.Context, string) (map[string]*ecdsa.PublicKey, error) {
	return func(ctx context.Context, kid string) (map[string]*ecdsa.PublicKey, error) {
		j, err := lookup(ctx, kid)
		if err != nil {
			return nil, fmt.Errorf("look up key %q: %w", kid, err)
		}
		keys := map[string]*ecdsa.PublicKey{}
		if j != nil {
			if key, err := p256Key(*j, alg); err == nil {
				keys[kid] = key
			}
		}
		return keys, nil
	}
}

// lookupSet fetches a key set. Keys the verifier cannot use, or without a
// kid, are left out; of two keys with one kid, the first is kept.
func lookupSet(fetch serviceauth.Fetcher, url, alg string) func(context.Context, string) (map[string]*ecdsa.PublicKey, error) {
	return func(ctx context.Context, _ string) (map[string]*ecdsa.PublicKey, error) {
		data, err := fetch(ctx, url, "")
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", url, err)
		}
		var set struct {
			Keys *[]serviceauth.JWK `json:"keys"`
		}
		if err := json.Unmarshal(data, &set); err != nil {
			return nil, fmt.Errorf("key set from %s: %w", url, err)
		}
		if set.Keys == nil {
			return nil, fmt.Errorf("key set from %s has no keys member", url)
		}
		keys := map[string]*ecdsa.PublicKey{}
		for _, j := range *set.Keys {
			if _, dup := keys[j.Kid]; j.Kid == "" || dup {
				continue
			}
			if key, err := p256Key(j, alg); err == nil {
				keys[j.Kid] = key
			}
		}
		return keys, nil
	}
}

type heldKey struct {
	key       *ecdsa.PublicKey
	fetchedAt time.Time
}

// keyCache holds the provider's keys by kid; JWTBodyHashVerifier's comment
// states its rules. A lookup answers the keys it found: for a KeyLookup the
// kid's, or none, which drops a held one; for a key set every key, which
// replaces those held.
type keyCache struct {
	lookup   func(ctx context.Context, kid string) (map[string]*ecdsa.PublicKey, error)
	replaces bool
	now      func() time.Time

	mu        sync.Mutex
	held      map[string]heldKey
	attempted time.Time
	inflight  chan struct{} // closed when the running lookup ends
}

func (c *keyCache) key(ctx context.Context, kid string) (*ecdsa.PublicKey, error) {
	for {
		c.mu.Lock()
		now := c.now()
		held, known := c.held[kid]
		if known && now.Sub(held.fetchedAt) < keyMaxAge {
			c.mu.Unlock()
			return held.key, nil
		}
		if wait := c.inflight; wait != nil {
			c.mu.Unlock()
			if known {
				return held.key, nil
			}
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, &refusal{http.StatusServiceUnavailable, CodeKeyUnavailable, DetailKeyUnavailable, ctx.Err()}
			}
		}
		if !c.attempted.IsZero() && now.Sub(c.attempted) < lookupMinInterval {
			c.mu.Unlock()
			if known {
				return held.key, nil
			}
			return nil, invalid("no key %q, and the provider was asked less than %s ago", kid, lookupMinInterval)
		}
		done := make(chan struct{})
		c.attempted = now
		c.inflight = done
		c.mu.Unlock()

		lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lookupTimeout)
		found, err := c.lookup(lookupCtx, kid)
		cancel()

		c.mu.Lock()
		switch {
		case err != nil:
		case c.replaces:
			c.held = map[string]heldKey{}
			for k, key := range found {
				c.held[k] = heldKey{key, now}
			}
		case found[kid] != nil:
			c.held[kid] = heldKey{found[kid], now}
		default:
			delete(c.held, kid)
		}
		c.inflight = nil
		close(done)
		_, holds := c.held[kid]
		c.mu.Unlock()
		if err != nil && !holds {
			return nil, &refusal{http.StatusServiceUnavailable, CodeKeyUnavailable, DetailKeyUnavailable, err}
		}
		// Look again: the lookup may have brought the kid, or not.
	}
}
