package webhook_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parable-work/superschematic/runtime/http/go/serviceauth"
	"github.com/parable-work/superschematic/runtime/http/go/webhook"
)

// clock is a settable test clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// plaidToken is a token for plaidBody under kid, signed by key at now.
func plaidToken(t testing.TB, key testKey, kid string, now time.Time) string {
	t.Helper()
	header := compact(t, object{{"alg", "ES256"}, {"kid", kid}, {"typ", "JWT"}})
	claims := compact(t, object{{"iat", now.Unix()}, {"request_body_sha256", hexSHA256(plaidBody)}})
	input := b64(header) + "." + b64(claims)
	return input + "." + b64(key.sign(t, input))
}

func options(keys webhook.KeyLookup, now func() time.Time) webhook.JWTBodyHashOptions {
	return webhook.JWTBodyHashOptions{
		Header:        "Plaid-Verification",
		Keys:          keys,
		Alg:           webhook.AlgES256,
		MaxAge:        5 * time.Minute,
		BodyHashClaim: "request_body_sha256",
		Now:           now,
	}
}

func post(t testing.TB, handler http.Handler, tok string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(plaidBody))
	req.Header.Set("Plaid-Verification", tok)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestJWTBodyHashVerifierRefusesABadConfig(t *testing.T) {
	lookup := func(context.Context, string) (*serviceauth.JWK, error) { return nil, nil }
	for name, tc := range map[string]struct {
		change func(*webhook.JWTBodyHashOptions)
		want   string
	}{
		"no header":            {func(o *webhook.JWTBodyHashOptions) { o.Header = "" }, "Header is required"},
		"another alg":          {func(o *webhook.JWTBodyHashOptions) { o.Alg = "RS256" }, `Alg "RS256" is not supported; use ES256`},
		"alg none":             {func(o *webhook.JWTBodyHashOptions) { o.Alg = "none" }, `Alg "none" is not supported`},
		"no max age":           {func(o *webhook.JWTBodyHashOptions) { o.MaxAge = 0 }, "MaxAge must be positive"},
		"no claim":             {func(o *webhook.JWTBodyHashOptions) { o.BodyHashClaim = "" }, "BodyHashClaim is required"},
		"a negative skew":      {func(o *webhook.JWTBodyHashOptions) { o.ClockSkew = -time.Second }, "ClockSkew must not be negative"},
		"no keys":              {func(o *webhook.JWTBodyHashOptions) { o.Keys = nil }, "set one of Keys and JWKSURL"},
		"both keys and a JWKS": {func(o *webhook.JWTBodyHashOptions) { o.JWKSURL = "https://keys.example" }, "set one of Keys and JWKSURL"},
	} {
		t.Run(name, func(t *testing.T) {
			opts := options(lookup, nil)
			tc.change(&opts)
			if _, err := webhook.JWTBodyHashVerifier(opts); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

// generatedVerifier is the WebhookVerifier type a generated Go server
// declares, which its WebhookVerifiers map holds.
type generatedVerifier func(http.Handler) http.Handler

// A held key serves without a lookup, and the next handler reads the body
// the verifier read, with its length.
func TestJWTBodyHashVerifierHoldsAKey(t *testing.T) {
	key := newTestKey(t)
	c := &clock{now: time.Unix(parityNow, 0)}
	var lookups atomic.Int32
	verify, err := webhook.JWTBodyHashVerifier(options(func(_ context.Context, kid string) (*serviceauth.JWK, error) {
		lookups.Add(1)
		j := key.jwk(kid)
		return &j, nil
	}, c.Now))
	if err != nil {
		t.Fatal(err)
	}
	// It goes into a generated server's map as it is.
	_ = map[string]generatedVerifier{"plaid": verify}
	var length int64
	handler := verify(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		length = r.ContentLength
		echo(t).ServeHTTP(w, r)
	}))
	for range 3 {
		rec := post(t, handler, plaidToken(t, key, "k1", c.Now()))
		if rec.Code != http.StatusOK || rec.Body.String() != plaidBody || length != int64(len(plaidBody)) {
			t.Fatalf("status %d, body %q, length %d", rec.Code, rec.Body.String(), length)
		}
	}
	if n := lookups.Load(); n != 1 {
		t.Fatalf("%d lookups, want 1", n)
	}
}

// The provider is asked about a kid not held at most once a minute, so a
// flood of random kids costs one lookup; a held kid still verifies
// meanwhile.
func TestJWTBodyHashVerifierBoundsLookups(t *testing.T) {
	key := newTestKey(t)
	c := &clock{now: time.Unix(parityNow, 0)}
	var asked []string
	var mu sync.Mutex
	verify, err := webhook.JWTBodyHashVerifier(options(func(_ context.Context, kid string) (*serviceauth.JWK, error) {
		mu.Lock()
		asked = append(asked, kid)
		mu.Unlock()
		if kid != "k1" {
			return nil, nil
		}
		j := key.jwk(kid)
		return &j, nil
	}, c.Now))
	if err != nil {
		t.Fatal(err)
	}
	handler := verify(echo(t))
	if rec := post(t, handler, plaidToken(t, key, "k1", c.Now())); rec.Code != http.StatusOK {
		t.Fatalf("k1: %d", rec.Code)
	}
	c.advance(time.Minute)
	for _, kid := range []string{"r1", "r2", "r3", "r4"} {
		if rec := post(t, handler, plaidToken(t, key, kid, c.Now())); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d", kid, rec.Code)
		}
	}
	if rec := post(t, handler, plaidToken(t, key, "k1", c.Now())); rec.Code != http.StatusOK {
		t.Fatalf("k1 during the flood: %d", rec.Code)
	}
	if want := []string{"k1", "r1"}; strings.Join(asked, ",") != strings.Join(want, ",") {
		t.Fatalf("asked %v, want %v", asked, want)
	}
	c.advance(time.Minute)
	post(t, handler, plaidToken(t, key, "r5", c.Now()))
	if len(asked) != 3 {
		t.Fatalf("asked %v after a minute, want r5 asked", asked)
	}
}

// A held key an hour old is looked up again. A lookup that fails keeps it;
// one that answers no key drops it.
func TestJWTBodyHashVerifierRefreshesAHeldKey(t *testing.T) {
	key := newTestKey(t)
	c := &clock{now: time.Unix(parityNow, 0)}
	answer := "key"
	var lookups atomic.Int32
	verify, err := webhook.JWTBodyHashVerifier(options(func(_ context.Context, kid string) (*serviceauth.JWK, error) {
		lookups.Add(1)
		switch answer {
		case "down":
			return nil, errors.New("down")
		case "gone":
			return nil, nil
		}
		j := key.jwk(kid)
		return &j, nil
	}, c.Now))
	if err != nil {
		t.Fatal(err)
	}
	handler := verify(echo(t))
	status := func() int { return post(t, handler, plaidToken(t, key, "k1", c.Now())).Code }
	if got := status(); got != http.StatusOK {
		t.Fatalf("first: %d", got)
	}
	c.advance(time.Hour)
	answer = "down"
	if got := status(); got != http.StatusOK || lookups.Load() != 2 {
		t.Fatalf("a failed refresh: %d after %d lookups, want 200 after 2", got, lookups.Load())
	}
	c.advance(time.Minute)
	answer = "gone"
	if got := status(); got != http.StatusUnauthorized || lookups.Load() != 3 {
		t.Fatalf("a key gone: %d after %d lookups, want 401 after 3", got, lookups.Load())
	}
}

// Requests for one kid not held share the lookup that is running.
func TestJWTBodyHashVerifierSharesALookup(t *testing.T) {
	key := newTestKey(t)
	c := &clock{now: time.Unix(parityNow, 0)}
	release := make(chan struct{})
	var lookups atomic.Int32
	verify, err := webhook.JWTBodyHashVerifier(options(func(_ context.Context, kid string) (*serviceauth.JWK, error) {
		lookups.Add(1)
		<-release
		j := key.jwk(kid)
		return &j, nil
	}, c.Now))
	if err != nil {
		t.Fatal(err)
	}
	handler := verify(echo(t))
	tok := plaidToken(t, key, "k1", c.Now())
	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		wg.Go(func() { codes[i] = post(t, handler, tok).Code })
	}
	for lookups.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(10 * time.Millisecond)
	close(release)
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("request %d: %d", i, code)
		}
	}
	if n := lookups.Load(); n != 1 {
		t.Fatalf("%d lookups, want 1", n)
	}
}

// With a JWKS URL the set is fetched whole: one fetch brings every key, a
// kid not in it is refused, and the next fetch waits a minute.
func TestJWTBodyHashVerifierFetchesAKeySet(t *testing.T) {
	k1, k2 := newTestKey(t), newTestKey(t)
	c := &clock{now: time.Unix(parityNow, 0)}
	var fetches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches.Add(1)
		_ = json.NewEncoder(w).Encode(serviceauth.JWKS{Keys: []serviceauth.JWK{k1.jwk("k1"), k2.jwk("k2"), {Kty: "RSA", Kid: "rsa", N: "AQAB", E: "AQAB"}}})
	}))
	t.Cleanup(server.Close)
	opts := options(nil, c.Now)
	opts.JWKSURL = server.URL
	verify, err := webhook.JWTBodyHashVerifier(opts)
	if err != nil {
		t.Fatal(err)
	}
	handler := verify(echo(t))
	for _, tc := range []struct {
		key  testKey
		kid  string
		want int
	}{{k1, "k1", 200}, {k2, "k2", 200}, {k1, "k3", 401}, {k1, "rsa", 401}, {k1, "k3", 401}} {
		if rec := post(t, handler, plaidToken(t, tc.key, tc.kid, c.Now())); rec.Code != tc.want {
			t.Fatalf("%s: %d, want %d", tc.kid, rec.Code, tc.want)
		}
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("%d fetches, want 1", n)
	}
	c.advance(time.Minute)
	post(t, handler, plaidToken(t, k1, "k3", c.Now()))
	if n := fetches.Load(); n != 2 {
		t.Fatalf("%d fetches after a minute, want 2", n)
	}
}

// Two of the header are refused, as in Rust; TypeScript's Headers joins
// them into a value that is no JWS.
func TestJWTBodyHashVerifierRefusesTwoHeaders(t *testing.T) {
	key := newTestKey(t)
	now := time.Unix(parityNow, 0)
	verify, err := webhook.JWTBodyHashVerifier(options(func(_ context.Context, kid string) (*serviceauth.JWK, error) {
		j := key.jwk(kid)
		return &j, nil
	}, func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	tok := plaidToken(t, key, "k1", now)
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(plaidBody))
	req.Header.Add("Plaid-Verification", tok)
	req.Header.Add("Plaid-Verification", tok)
	rec := httptest.NewRecorder()
	verify(echo(t)).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
}

// A key the verifier cannot use is no key: another curve, a private key, or
// one whose use or alg names something else.
func TestJWTBodyHashVerifierRefusesAKeyItCannotUse(t *testing.T) {
	key := newTestKey(t)
	now := time.Unix(parityNow, 0)
	for name, change := range map[string]func(*serviceauth.JWK){
		"another curve": func(j *serviceauth.JWK) { j.Crv = "P-384" },
		"a private key": func(j *serviceauth.JWK) { j.D = "AAAA" },
		"use enc":       func(j *serviceauth.JWK) { j.Use = "enc" },
		"alg RS256":     func(j *serviceauth.JWK) { j.Alg = "RS256" },
		"short x":       func(j *serviceauth.JWK) { j.X = "AAAA" },
	} {
		t.Run(name, func(t *testing.T) {
			verify, err := webhook.JWTBodyHashVerifier(options(func(_ context.Context, kid string) (*serviceauth.JWK, error) {
				j := key.jwk(kid)
				change(&j)
				return &j, nil
			}, func() time.Time { return now }))
			if err != nil {
				t.Fatal(err)
			}
			if rec := post(t, verify(echo(t)), plaidToken(t, key, "k1", now)); rec.Code != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401", rec.Code)
			}
		})
	}
}
