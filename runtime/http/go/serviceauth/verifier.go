package serviceauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// HeaderName is the header that carries a service credential.
const HeaderName = "Service-Authorization"

// Caller is the deployable calling this server, as a service credential
// establishes it.
type Caller struct {
	// Deployable is the calling deployable's name in the environment.
	Deployable string `json:"deployable"`
	// Serves lists the APIs the deployable serves; a route's from is
	// checked against it.
	Serves []string `json:"serves"`
	// Subject is the credential's subject claim, for logs.
	Subject string `json:"subject"`
}

// Authenticator establishes the service caller of a request. It returns
// (nil, nil) when the request carries no service credential, and an *Error
// when it refuses one: 401 service_unauthorized for a credential that does
// not verify, 403 service_forbidden for an identity that is no caller of
// this server, 503 service_unavailable for a failure that is not the
// caller's. It reads only the Service-Authorization header. Verifier is the
// standard implementation; a deployment whose credential a Config cannot
// express passes its own.
type Authenticator interface {
	Authenticate(r *http.Request) (*Caller, error)
}

// Fetcher fetches a JWKS document. bearer, when not empty, is sent as
// "Authorization: Bearer <bearer>".
type Fetcher func(ctx context.Context, url, bearer string) ([]byte, error)

// Option configures a Verifier or a TokenSource. Each constructor reads the
// options it documents and ignores the rest.
type Option func(*options)

type options struct {
	now    func() time.Time
	fetch  Fetcher
	client *http.Client
	random io.Reader
}

// WithClock replaces time.Now: the verifier's clock for exp, nbf, iat and
// the key cache, and a token source's for its cache.
func WithClock(now func() time.Time) Option {
	return func(o *options) { o.now = now }
}

// WithFetcher replaces the verifier's JWKS fetcher, by default an
// HTTPFetcher.
func WithFetcher(fetch Fetcher) Option {
	return func(o *options) { o.fetch = fetch }
}

// WithHTTPClient sets the HTTP client of the default fetcher and of
// GoogleIDToken. The default has a 10 second timeout.
func WithHTTPClient(client *http.Client) Option {
	return func(o *options) { o.client = client }
}

// WithRandom replaces crypto/rand as the source of SignedToken's jti.
func WithRandom(random io.Reader) Option {
	return func(o *options) { o.random = random }
}

func buildOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if o.now == nil {
		o.now = time.Now
	}
	if o.client == nil {
		o.client = &http.Client{Timeout: 10 * time.Second}
	}
	if o.fetch == nil {
		o.fetch = HTTPFetcher(o.client)
	}
	return o
}

// maxJWKSBytes bounds a fetched key set.
const maxJWKSBytes = 1 << 20

// HTTPFetcher is the default Fetcher: a GET with the client, which must
// answer 200 with at most 1 MiB.
func HTTPFetcher(client *http.Client) Fetcher {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return func(ctx context.Context, url, bearer string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes+1))
		if err != nil {
			return nil, fmt.Errorf("GET %s: %w", url, err)
		}
		if len(body) > maxJWKSBytes {
			return nil, fmt.Errorf("GET %s: key set larger than %d bytes", url, maxJWKSBytes)
		}
		return body, nil
	}
}

// defaultLeeway is the clock skew allowed when the config sets none.
const defaultLeeway = 60

// Verifier is the standard Authenticator: it verifies the JWT in
// Service-Authorization against a Config. It is safe for concurrent use.
type Verifier struct {
	issuers map[string]*issuer
	leeway  int64
	now     func() time.Time
	fetch   Fetcher
}

// issuer is a config entry, ready to verify with.
type issuer struct {
	audience     string
	algorithms   map[string]bool
	subjectClaim string
	maxLifetime  int64
	callers      map[string]CallerConfig
	static       map[string]publicKey
	jwks         *keyCache
	bearerFile   string
}

// New builds a Verifier from cfg. It refuses a config it could not verify
// with: an entry without an issuer, audience or algorithms, an algorithm
// other than RS256, ES256 and EdDSA, both or neither of jwksUrl and keys, a
// static key it cannot read or that has no kid, an issuer or alias two
// entries share, or a caller with no deployable. It reads WithClock,
// WithFetcher and WithHTTPClient.
func New(cfg Config, opts ...Option) (*Verifier, error) {
	o := buildOptions(opts)
	v := &Verifier{
		issuers: map[string]*issuer{},
		leeway:  defaultLeeway,
		now:     o.now,
		fetch:   o.fetch,
	}
	if cfg.LeewaySeconds != nil {
		if *cfg.LeewaySeconds < 0 {
			return nil, errors.New("serviceauth: leewaySeconds is negative")
		}
		v.leeway = *cfg.LeewaySeconds
	}
	caches := map[string]*keyCache{}
	for i, entry := range cfg.Issuers {
		iss, err := newIssuer(entry, caches)
		if err != nil {
			return nil, fmt.Errorf("serviceauth: issuers[%d] (%q): %w", i, entry.Issuer, err)
		}
		for _, name := range append([]string{entry.Issuer}, entry.IssuerAliases...) {
			if name == "" {
				return nil, fmt.Errorf("serviceauth: issuers[%d]: empty issuer or alias", i)
			}
			if _, dup := v.issuers[name]; dup {
				return nil, fmt.Errorf("serviceauth: issuers[%d]: issuer %q is already another entry's", i, name)
			}
			v.issuers[name] = iss
		}
	}
	return v, nil
}

func newIssuer(entry IssuerConfig, caches map[string]*keyCache) (*issuer, error) {
	if entry.Audience == "" {
		return nil, errors.New("no audience")
	}
	if len(entry.Algorithms) == 0 {
		return nil, errors.New("no algorithms")
	}
	iss := &issuer{
		audience:     entry.Audience,
		algorithms:   map[string]bool{},
		subjectClaim: entry.SubjectClaim,
		maxLifetime:  entry.MaxLifetimeSeconds,
		callers:      map[string]CallerConfig{},
	}
	for _, alg := range entry.Algorithms {
		switch alg {
		case AlgRS256, AlgES256, AlgEdDSA:
			iss.algorithms[alg] = true
		default:
			return nil, fmt.Errorf("algorithm %q is not RS256, ES256 or EdDSA", alg)
		}
	}
	if iss.subjectClaim == "" {
		iss.subjectClaim = "sub"
	}
	if iss.maxLifetime < 0 {
		return nil, errors.New("maxLifetimeSeconds is negative")
	}
	switch {
	case entry.JWKSURL != "" && len(entry.Keys) > 0:
		return nil, errors.New("both jwksUrl and keys")
	case entry.JWKSURL != "":
		cache, ok := caches[entry.JWKSURL]
		if !ok {
			cache = &keyCache{url: entry.JWKSURL}
			caches[entry.JWKSURL] = cache
		}
		iss.jwks = cache
		iss.bearerFile = entry.JWKSBearerTokenFile
	case len(entry.Keys) > 0:
		if entry.JWKSBearerTokenFile != "" {
			return nil, errors.New("jwksBearerTokenFile without jwksUrl")
		}
		iss.static = map[string]publicKey{}
		for i, j := range entry.Keys {
			if j.Kid == "" {
				return nil, fmt.Errorf("keys[%d] has no kid", i)
			}
			if _, dup := iss.static[j.Kid]; dup {
				return nil, fmt.Errorf("keys[%d]: kid %q repeats", i, j.Kid)
			}
			key, err := parsePublicJWK(j)
			if err != nil {
				return nil, fmt.Errorf("keys[%d] (%q): %w", i, j.Kid, err)
			}
			iss.static[j.Kid] = key
		}
	default:
		return nil, errors.New("neither jwksUrl nor keys")
	}
	for subject, c := range entry.Callers {
		if c.Deployable == "" {
			return nil, fmt.Errorf("caller %q has no deployable", subject)
		}
		iss.callers[subject] = CallerConfig{Deployable: c.Deployable, Serves: slices.Clone(c.Serves)}
	}
	return iss, nil
}

// Authenticate verifies the request's Service-Authorization credential. It
// follows the algorithm every runtime shares (the D37 contract):
//
//  1. no header: no caller;
//  2. not one "Bearer <token>" (scheme in any case, one token): 401;
//  3. not three unpadded base64url segments whose first two are JSON
//     objects: 401;
//  4. the payload's iss selects the entry by issuer or alias; none: 401;
//  5. the header's alg (Ed25519 read as EdDSA) not in the entry's
//     algorithms, or no kid: 401;
//  6. the key with that kid, static or from the JWKS cache: none fetched
//     and none cached is 503; kid unknown, or a key whose type does not fit
//     alg, is 401;
//  7. the signature over "<header>.<payload>" does not verify: 401;
//  8. the claims, with the leeway L: exp a number and now < exp + L; nbf
//     and iat, when present, numbers no later than now + L; aud, a string
//     or array of strings, holding the entry's audience; with a maximum
//     lifetime M, iat present and exp - iat <= M. Else 401;
//  9. the subject claim names a caller in the entry: else 403.
func (v *Verifier) Authenticate(r *http.Request) (*Caller, error) {
	values := r.Header.Values(HeaderName)
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > 1 {
		return nil, Invalid(errors.New("more than one Service-Authorization header"))
	}
	token, ok := parseBearer(values[0])
	if !ok {
		return nil, Invalid(errors.New("not a Bearer credential"))
	}
	return v.verify(r.Context(), token)
}

// parseBearer reads "Bearer <token>": the scheme in any case, one space,
// and one token with no whitespace in it.
func parseBearer(value string) (string, bool) {
	scheme, token, found := strings.Cut(value, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" || strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}

func (v *Verifier) verify(ctx context.Context, token string) (*Caller, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, Invalid(fmt.Errorf("token has %d segments, not 3", len(parts)))
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, Invalid(fmt.Errorf("header segment: %w", err))
	}
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, Invalid(fmt.Errorf("payload segment: %w", err))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, Invalid(fmt.Errorf("signature segment: %w", err))
	}
	header, err := jsonObject(headerJSON)
	if err != nil {
		return nil, Invalid(fmt.Errorf("header: %w", err))
	}
	claims, err := jsonObject(payloadJSON)
	if err != nil {
		return nil, Invalid(fmt.Errorf("payload: %w", err))
	}

	issValue, _ := claims["iss"].(string)
	iss, ok := v.issuers[issValue]
	if !ok {
		return nil, Invalid(fmt.Errorf("issuer %q is not configured", issValue))
	}

	alg, _ := header["alg"].(string)
	if alg == AlgEd25519 {
		alg = AlgEdDSA
	}
	if !iss.algorithms[alg] {
		return nil, Invalid(fmt.Errorf("alg %q is not accepted for issuer %q", alg, issValue))
	}
	kid, _ := header["kid"].(string)
	if kid == "" {
		return nil, Invalid(errors.New("header has no kid"))
	}

	key, err := v.key(ctx, iss, kid)
	if err != nil {
		return nil, err
	}
	if !key.fits(alg) {
		return nil, Invalid(fmt.Errorf("key %q does not fit alg %s", kid, alg))
	}
	if !key.verify(alg, []byte(parts[0]+"."+parts[1]), sig) {
		return nil, Invalid(errors.New("signature does not verify"))
	}

	if err := v.checkClaims(iss, claims); err != nil {
		return nil, Invalid(err)
	}

	subject, _ := claims[iss.subjectClaim].(string)
	caller, ok := iss.callers[subject]
	if !ok {
		return nil, Forbidden(fmt.Errorf("%s %q of issuer %q is not a caller", iss.subjectClaim, subject, issValue))
	}
	return &Caller{
		Deployable: caller.Deployable,
		Serves:     slices.Clone(caller.Serves),
		Subject:    subject,
	}, nil
}

// jsonObject decodes a JSON object; any other JSON value, or trailing data,
// is an error.
func jsonObject(data []byte) (map[string]any, error) {
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, errors.New("not a JSON object")
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("data after the JSON object")
	}
	return obj, nil
}

// checkClaims applies step 8: the time claims with the leeway, the
// audience, and the maximum lifetime.
func (v *Verifier) checkClaims(iss *issuer, claims map[string]any) error {
	now := float64(v.now().Unix())
	leeway := float64(v.leeway)

	exp, ok := claims["exp"].(float64)
	if !ok {
		return errors.New("exp is missing or not a number")
	}
	if now >= exp+leeway {
		return errors.New("token has expired")
	}
	if raw, present := claims["nbf"]; present {
		nbf, ok := raw.(float64)
		if !ok {
			return errors.New("nbf is not a number")
		}
		if now+leeway < nbf {
			return errors.New("token is not valid yet (nbf)")
		}
	}
	iat, hasIat := claims["iat"]
	var issuedAt float64
	if hasIat {
		issuedAt, ok = iat.(float64)
		if !ok {
			return errors.New("iat is not a number")
		}
		if now+leeway < issuedAt {
			return errors.New("token is not valid yet (iat)")
		}
	}
	if !audienceHolds(claims["aud"], iss.audience) {
		return fmt.Errorf("aud does not hold %q", iss.audience)
	}
	if iss.maxLifetime > 0 {
		if !hasIat {
			return errors.New("iat is required with a maximum lifetime")
		}
		if exp-issuedAt > float64(iss.maxLifetime) {
			return fmt.Errorf("lifetime %gs is over %ds", exp-issuedAt, iss.maxLifetime)
		}
	}
	return nil
}

// audienceHolds reports whether aud, a string or an array of strings, is or
// holds audience.
func audienceHolds(aud any, audience string) bool {
	switch a := aud.(type) {
	case string:
		return a == audience
	case []any:
		found := false
		for _, member := range a {
			s, ok := member.(string)
			if !ok {
				return false
			}
			if s == audience {
				found = true
			}
		}
		return found
	}
	return false
}

// key finds the key a token names: static, or from the issuer's key set.
func (v *Verifier) key(ctx context.Context, iss *issuer, kid string) (publicKey, error) {
	if iss.static != nil {
		key, ok := iss.static[kid]
		if !ok {
			return publicKey{}, Invalid(fmt.Errorf("kid %q is not a configured key", kid))
		}
		return key, nil
	}
	return iss.jwks.lookup(ctx, v, iss.bearerFile, kid)
}
