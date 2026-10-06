package serviceauth_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parable-work/superschematic/runtime/http/go/serviceauth"
)

// fakeClock is a settable clock.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(unix int64) *fakeClock { return &fakeClock{t: time.Unix(unix, 0)} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// stubKeys serves key sets by URL and counts fetches.
type stubKeys struct {
	mu      sync.Mutex
	sets    map[string]any
	down    bool
	calls   int
	bearers []string
	gate    chan struct{} // when set, each fetch waits for it
}

func (s *stubKeys) set(url string, set any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sets[url] = set
}

func (s *stubKeys) setDown(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = down
}

func (s *stubKeys) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *stubKeys) fetch(_ context.Context, url, bearer string) ([]byte, error) {
	s.mu.Lock()
	s.calls++
	s.bearers = append(s.bearers, bearer)
	gate := s.gate
	set, ok := s.sets[url]
	down := s.down
	s.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if down || !ok {
		return nil, errors.New("unreachable")
	}
	if raw, isRaw := set.(string); isRaw {
		return []byte(raw), nil
	}
	return json.Marshal(set)
}

// request is a request carrying token in Service-Authorization.
func request(token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if token != "" {
		r.Header.Set(serviceauth.HeaderName, "Bearer "+token)
	}
	return r
}

// refusal returns the status and code of an authenticator error, or 0 and
// "" for none.
func refusal(t *testing.T, err error) (int, string) {
	t.Helper()
	if err == nil {
		return 0, ""
	}
	var e *serviceauth.Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not a *serviceauth.Error", err)
	}
	return e.Status, e.Code
}

func googleConfig(t *testing.T, keys parityKeys) serviceauth.Config {
	t.Helper()
	return parityConfigs(keys)["google"]
}

// googleToken is a Google ID token for the orders account, valid from iat
// for an hour.
func googleToken(t *testing.T, key *testKey, iat int64) string {
	t.Helper()
	claims := object{{"iss", googleIssuer}, {"aud", googleAudience}, {"sub", ordersAccount}, {"iat", iat}, {"exp", iat + 3600}}
	return key.jwt(t, serviceauth.AlgRS256, key.header(serviceauth.AlgRS256), claims)
}

func TestNewRefusesConfigsItCannotVerifyWith(t *testing.T) {
	keys := newParityKeys(t)
	edge := keys.edgeOrders.public()
	withD := edge
	withD.D = b64(keys.edgeOrders.ed.Seed())
	shortRSA := keys.google.public()
	shortRSA.N = b64(keys.google.rsa.N.Bytes()[:128])
	p384 := keys.kubeEC.public()
	p384.Crv = "P-384"
	callers := map[string]serviceauth.CallerConfig{"orders": {Deployable: "orders", Serves: []string{"shop-orders"}}}
	entry := func(mutate func(*serviceauth.IssuerConfig)) serviceauth.Config {
		e := serviceauth.IssuerConfig{Issuer: "orders", Audience: "shop-api", Algorithms: []string{"EdDSA"}, Keys: []serviceauth.JWK{edge}, Callers: callers}
		mutate(&e)
		return serviceauth.Config{Issuers: []serviceauth.IssuerConfig{e}}
	}
	negative := int64(-1)
	cases := []struct {
		name string
		cfg  serviceauth.Config
		want string
	}{
		{"no issuer", entry(func(e *serviceauth.IssuerConfig) { e.Issuer = "" }), "empty issuer"},
		{"no audience", entry(func(e *serviceauth.IssuerConfig) { e.Audience = "" }), "no audience"},
		{"no algorithms", entry(func(e *serviceauth.IssuerConfig) { e.Algorithms = nil }), "no algorithms"},
		{"HS256", entry(func(e *serviceauth.IssuerConfig) { e.Algorithms = []string{"HS256"} }), `"HS256"`},
		{"none", entry(func(e *serviceauth.IssuerConfig) { e.Algorithms = []string{"none"} }), `"none"`},
		{"Ed25519 is a header spelling only", entry(func(e *serviceauth.IssuerConfig) { e.Algorithms = []string{"Ed25519"} }), `"Ed25519"`},
		{"jwksUrl and keys", entry(func(e *serviceauth.IssuerConfig) { e.JWKSURL = "https://keys.test" }), "both jwksUrl and keys"},
		{"neither jwksUrl nor keys", entry(func(e *serviceauth.IssuerConfig) { e.Keys = nil }), "neither jwksUrl nor keys"},
		{"bearer file without jwksUrl", entry(func(e *serviceauth.IssuerConfig) { e.JWKSBearerTokenFile = "/token" }), "jwksBearerTokenFile without jwksUrl"},
		{"key without kid", entry(func(e *serviceauth.IssuerConfig) { k := edge; k.Kid = ""; e.Keys = []serviceauth.JWK{k} }), "has no kid"},
		{"kid twice", entry(func(e *serviceauth.IssuerConfig) { e.Keys = []serviceauth.JWK{edge, edge} }), "repeats"},
		{"private key", entry(func(e *serviceauth.IssuerConfig) { e.Keys = []serviceauth.JWK{withD} }), "private member"},
		{"short RSA key", entry(func(e *serviceauth.IssuerConfig) { e.Keys = []serviceauth.JWK{shortRSA} }), "1024 bits"},
		{"P-384 key", entry(func(e *serviceauth.IssuerConfig) { e.Keys = []serviceauth.JWK{p384} }), "not P-256"},
		{"oct key", entry(func(e *serviceauth.IssuerConfig) { e.Keys = []serviceauth.JWK{{Kty: "oct", Kid: "k"}} }), `"oct" is not supported`},
		{"caller without deployable", entry(func(e *serviceauth.IssuerConfig) {
			e.Callers = map[string]serviceauth.CallerConfig{"orders": {}}
		}), "no deployable"},
		{"negative max lifetime", entry(func(e *serviceauth.IssuerConfig) { e.MaxLifetimeSeconds = -1 }), "negative"},
		{"empty alias", entry(func(e *serviceauth.IssuerConfig) { e.IssuerAliases = []string{""} }), "empty issuer or alias"},
		{"negative leeway", serviceauth.Config{LeewaySeconds: &negative}, "leewaySeconds is negative"},
		{"issuer two entries share", serviceauth.Config{Issuers: []serviceauth.IssuerConfig{
			entry(func(*serviceauth.IssuerConfig) {}).Issuers[0],
			entry(func(e *serviceauth.IssuerConfig) { e.Issuer = "billing"; e.IssuerAliases = []string{"orders"} }).Issuers[0],
		}}, "already another entry's"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := serviceauth.New(tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New error = %v, want one containing %q", err, tc.want)
			}
		})
	}

	t.Run("a config with no issuers verifies nothing", func(t *testing.T) {
		v, err := serviceauth.New(serviceauth.Config{})
		if err != nil {
			t.Fatal(err)
		}
		if status, code := refusal(t, func() error {
			_, err := v.Authenticate(request(signedToken(t, keys.edgeOrders, "orders", time.Now().Unix())))
			return err
		}()); status != 401 || code != serviceauth.CodeUnauthorized {
			t.Fatalf("got %d %s, want 401 service_unauthorized", status, code)
		}
	})
}

func TestVerifierReadsTheSubjectClaimAndLeeway(t *testing.T) {
	keys := newParityKeys(t)
	zero := int64(0)
	cfg := serviceauth.Config{LeewaySeconds: &zero, Issuers: []serviceauth.IssuerConfig{{
		Issuer:       googleIssuer,
		Audience:     googleAudience,
		Algorithms:   []string{serviceauth.AlgRS256},
		Keys:         []serviceauth.JWK{keys.google.public()},
		SubjectClaim: "email",
		Callers: map[string]serviceauth.CallerConfig{
			"orders@shop.iam.gserviceaccount.com": {Deployable: "orders", Serves: []string{"shop-orders"}},
		},
	}}}
	clock := newClock(parityNow)
	v, err := serviceauth.New(cfg, serviceauth.WithClock(clock.now))
	if err != nil {
		t.Fatal(err)
	}
	token := func(exp int64) string {
		claims := object{{"iss", googleIssuer}, {"aud", googleAudience}, {"sub", ordersAccount}, {"email", "orders@shop.iam.gserviceaccount.com"}, {"exp", exp}}
		return keys.google.jwt(t, serviceauth.AlgRS256, keys.google.header(serviceauth.AlgRS256), claims)
	}

	caller, err := v.Authenticate(request(token(parityNow + 1)))
	if err != nil {
		t.Fatal(err)
	}
	if caller.Subject != "orders@shop.iam.gserviceaccount.com" || caller.Deployable != "orders" {
		t.Fatalf("caller = %+v", caller)
	}
	caller.Serves[0] = "changed"
	again, err := v.Authenticate(request(token(parityNow + 1)))
	if err != nil || again.Serves[0] != "shop-orders" {
		t.Fatalf("a caller's Serves must be its own copy: %+v, %v", again, err)
	}

	// With no leeway, a token is refused the second its exp arrives.
	if status, _ := refusal(t, func() error { _, err := v.Authenticate(request(token(parityNow))); return err }()); status != 401 {
		t.Fatalf("exp == now with no leeway: status %d, want 401", status)
	}
}

func TestVerifierRefusesTwoServiceAuthorizationHeaders(t *testing.T) {
	keys := newParityKeys(t)
	v, err := serviceauth.New(googleConfig(t, keys), serviceauth.WithFetcher((&stubKeys{sets: map[string]any{}}).fetch))
	if err != nil {
		t.Fatal(err)
	}
	r := request("")
	r.Header.Add(serviceauth.HeaderName, "Bearer a.b.c")
	r.Header.Add(serviceauth.HeaderName, "Bearer a.b.c")
	if status, code := refusal(t, func() error { _, err := v.Authenticate(r); return err }()); status != 401 || code != serviceauth.CodeUnauthorized {
		t.Fatalf("got %d %s", status, code)
	}
	if caller, err := v.Authenticate(request("")); caller != nil || err != nil {
		t.Fatalf("no header: caller %v, error %v; want neither", caller, err)
	}
}

func TestKeyCacheFetchesOnceThenServesFromCache(t *testing.T) {
	keys := newParityKeys(t)
	stub := &stubKeys{sets: map[string]any{googleJWKS: serviceauth.JWKS{Keys: []serviceauth.JWK{keys.google.public()}}}}
	clock := newClock(parityNow)
	v, err := serviceauth.New(googleConfig(t, keys), serviceauth.WithClock(clock.now), serviceauth.WithFetcher(stub.fetch))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := v.Authenticate(request(googleToken(t, keys.google, clock.now().Unix()))); err != nil {
			t.Fatal(err)
		}
		clock.advance(10 * time.Minute)
	}
	// 40 minutes in: still the first fetch.
	if got := stub.count(); got != 1 {
		t.Fatalf("fetches = %d, want 1", got)
	}
	// Past an hour, the next request fetches again.
	clock.advance(30 * time.Minute)
	if _, err := v.Authenticate(request(googleToken(t, keys.google, clock.now().Unix()))); err != nil {
		t.Fatal(err)
	}
	if got := stub.count(); got != 2 {
		t.Fatalf("fetches after an hour = %d, want 2", got)
	}
}

func TestKeyCacheRefetchesForAnUnknownKidAtMostOncePerMinute(t *testing.T) {
	keys := newParityKeys(t)
	rotated := rsaTestKey(t, "google-2026-02", rsaKubeJWK)
	stub := &stubKeys{sets: map[string]any{googleJWKS: serviceauth.JWKS{Keys: []serviceauth.JWK{keys.google.public()}}}}
	clock := newClock(parityNow)
	v, err := serviceauth.New(googleConfig(t, keys), serviceauth.WithClock(clock.now), serviceauth.WithFetcher(stub.fetch))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Authenticate(request(googleToken(t, keys.google, parityNow))); err != nil {
		t.Fatal(err)
	}

	// The issuer rotates. Within the minute after the first fetch, the
	// new kid is unknown and nothing is fetched.
	stub.set(googleJWKS, serviceauth.JWKS{Keys: []serviceauth.JWK{keys.google.public(), rotated.public()}})
	clock.advance(30 * time.Second)
	if status, _ := refusal(t, func() error { _, err := v.Authenticate(request(googleToken(t, rotated, parityNow))); return err }()); status != 401 {
		t.Fatalf("unknown kid within the minute: status %d, want 401", status)
	}
	if got := stub.count(); got != 1 {
		t.Fatalf("fetches = %d, want 1", got)
	}

	// A minute after the last fetch, the unknown kid fetches the new set.
	clock.advance(30 * time.Second)
	if _, err := v.Authenticate(request(googleToken(t, rotated, parityNow))); err != nil {
		t.Fatalf("rotated key after a minute: %v", err)
	}
	if got := stub.count(); got != 2 {
		t.Fatalf("fetches = %d, want 2", got)
	}

	// A kid no set holds refetches once a minute, not per request.
	for i := 0; i < 3; i++ {
		clock.advance(time.Second)
		token := keys.google.jwt(t, serviceauth.AlgRS256, keys.google.header(serviceauth.AlgRS256).with("kid", "forged"), googleClaims(t))
		if status, _ := refusal(t, func() error { _, err := v.Authenticate(request(token)); return err }()); status != 401 {
			t.Fatalf("forged kid: status %d, want 401", status)
		}
	}
	if got := stub.count(); got != 2 {
		t.Fatalf("fetches after forged kids within a minute = %d, want 2", got)
	}
}

func googleClaims(t *testing.T) object {
	t.Helper()
	return object{{"iss", googleIssuer}, {"aud", googleAudience}, {"sub", ordersAccount}, {"iat", parityNow}, {"exp", parityNow + 3600}}
}

func TestKeyCacheKeepsKeysWhenAFetchFails(t *testing.T) {
	keys := newParityKeys(t)
	stub := &stubKeys{sets: map[string]any{googleJWKS: serviceauth.JWKS{Keys: []serviceauth.JWK{keys.google.public()}}}}
	clock := newClock(parityNow)
	v, err := serviceauth.New(googleConfig(t, keys), serviceauth.WithClock(clock.now), serviceauth.WithFetcher(stub.fetch))
	if err != nil {
		t.Fatal(err)
	}
	token := keys.google.jwt(t, serviceauth.AlgRS256, keys.google.header(serviceauth.AlgRS256),
		object{{"iss", googleIssuer}, {"aud", googleAudience}, {"sub", ordersAccount}, {"exp", parityNow + 10*3600}})
	if _, err := v.Authenticate(request(token)); err != nil {
		t.Fatal(err)
	}
	stub.setDown(true)
	clock.advance(2 * time.Hour)
	if _, err := v.Authenticate(request(token)); err != nil {
		t.Fatalf("stale keys must serve while the fetch fails: %v", err)
	}
	if got := stub.count(); got != 2 {
		t.Fatalf("fetches = %d, want 2", got)
	}
	unknown := keys.google.jwt(t, serviceauth.AlgRS256, keys.google.header(serviceauth.AlgRS256).with("kid", "other"),
		object{{"iss", googleIssuer}, {"aud", googleAudience}, {"sub", ordersAccount}, {"exp", parityNow + 10*3600}})
	clock.advance(time.Minute)
	if status, _ := refusal(t, func() error { _, err := v.Authenticate(request(unknown)); return err }()); status != 401 {
		t.Fatalf("unknown kid with keys cached and the fetch failing: status %d, want 401", status)
	}
}

func TestKeyCacheAnswers503UntilAFetchSucceeds(t *testing.T) {
	keys := newParityKeys(t)
	stub := &stubKeys{sets: map[string]any{googleJWKS: serviceauth.JWKS{Keys: []serviceauth.JWK{keys.google.public()}}}, down: true}
	clock := newClock(parityNow)
	v, err := serviceauth.New(googleConfig(t, keys), serviceauth.WithClock(clock.now), serviceauth.WithFetcher(stub.fetch))
	if err != nil {
		t.Fatal(err)
	}
	authenticate := func() (int, string) {
		_, err := v.Authenticate(request(googleToken(t, keys.google, clock.now().Unix())))
		return refusal(t, err)
	}
	if status, code := authenticate(); status != 503 || code != serviceauth.CodeUnavailable {
		t.Fatalf("got %d %s, want 503 service_unavailable", status, code)
	}
	clock.advance(10 * time.Second)
	if status, _ := authenticate(); status != 503 {
		t.Fatalf("status %d, want 503", status)
	}
	if got := stub.count(); got != 1 {
		t.Fatalf("fetches within the minute = %d, want 1", got)
	}
	stub.setDown(false)
	clock.advance(time.Minute)
	if status, _ := authenticate(); status != 0 {
		t.Fatalf("status %d after the key endpoint recovers, want none", status)
	}
}

func TestKeyCacheReadsTheKeySetStrictlyAndKeysLeniently(t *testing.T) {
	keys := newParityKeys(t)
	enc := keys.google.public()
	enc.Use = "enc"
	private := keys.edgeOrders.public()
	private.D = b64(keys.edgeOrders.ed.Seed())
	cases := []struct {
		name   string
		set    any
		status int
	}{
		{"a key for encryption is left out", serviceauth.JWKS{Keys: []serviceauth.JWK{enc}}, 401},
		{"unusable keys beside the good one", map[string]any{"keys": []any{
			map[string]any{"kty": "oct", "kid": "google-2026-01", "k": "c2VjcmV0"},
			private,
			keys.google.public(),
		}}, 0},
		{"members a key may carry are ignored", map[string]any{"keys": []any{
			map[string]any{"kty": "RSA", "kid": "google-2026-01", "n": keys.google.public().N, "e": "AQAB", "x5t": "abc", "key_ops": []string{"verify"}},
		}}, 0},
		{"a document that is not JSON", "<html>", 503},
		{"a document with no keys member", map[string]any{"jwks": []any{}}, 503},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubKeys{sets: map[string]any{googleJWKS: tc.set}}
			v, err := serviceauth.New(googleConfig(t, keys), serviceauth.WithClock(newClock(parityNow).now), serviceauth.WithFetcher(stub.fetch))
			if err != nil {
				t.Fatal(err)
			}
			_, err = v.Authenticate(request(googleToken(t, keys.google, parityNow)))
			if status, _ := refusal(t, err); status != tc.status {
				t.Fatalf("status %d (%v), want %d", status, err, tc.status)
			}
		})
	}
}

func TestKeyCacheSendsTheBearerTokenFile(t *testing.T) {
	keys := newParityKeys(t)
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("kube-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := parityConfigs(keys)["kubernetes"]
	cfg.Issuers[0].JWKSBearerTokenFile = tokenFile
	stub := &stubKeys{sets: map[string]any{kubeJWKS: serviceauth.JWKS{Keys: []serviceauth.JWK{keys.kubeRSA.public()}}}}
	v, err := serviceauth.New(cfg, serviceauth.WithClock(newClock(parityNow).now), serviceauth.WithFetcher(stub.fetch))
	if err != nil {
		t.Fatal(err)
	}
	claims := object{{"aud", []string{kubeAudience}}, {"exp", parityNow + 600}, {"iat", parityNow}, {"iss", kubeIssuer}, {"sub", kubeOrders}}
	if _, err := v.Authenticate(request(keys.kubeRSA.jwt(t, serviceauth.AlgRS256, keys.kubeRSA.header(serviceauth.AlgRS256), claims))); err != nil {
		t.Fatal(err)
	}
	if len(stub.bearers) != 1 || stub.bearers[0] != "kube-token" {
		t.Fatalf("bearers sent = %q, want [kube-token]", stub.bearers)
	}

	cfg.Issuers[0].JWKSBearerTokenFile = filepath.Join(dir, "missing")
	v, err = serviceauth.New(cfg, serviceauth.WithClock(newClock(parityNow).now), serviceauth.WithFetcher(stub.fetch))
	if err != nil {
		t.Fatal(err)
	}
	_, err = v.Authenticate(request(keys.kubeRSA.jwt(t, serviceauth.AlgRS256, keys.kubeRSA.header(serviceauth.AlgRS256), claims)))
	if status, _ := refusal(t, err); status != 503 {
		t.Fatalf("unreadable bearer file: status %d, want 503", status)
	}
}

// TestKeyCacheIsSafeForConcurrentUse runs many requests against a cold
// cache, a rotation and an hourly refresh, under -race: one fetch serves
// each, and no request fails.
func TestKeyCacheIsSafeForConcurrentUse(t *testing.T) {
	keys := newParityKeys(t)
	rotated := rsaTestKey(t, "google-2026-02", rsaKubeJWK)
	gate := make(chan struct{})
	stub := &stubKeys{sets: map[string]any{googleJWKS: serviceauth.JWKS{Keys: []serviceauth.JWK{keys.google.public()}}}, gate: gate}
	clock := newClock(parityNow)
	v, err := serviceauth.New(googleConfig(t, keys), serviceauth.WithClock(clock.now), serviceauth.WithFetcher(stub.fetch))
	if err != nil {
		t.Fatal(err)
	}
	run := func(token string, n int) []error {
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() {
				_, errs[i] = v.Authenticate(request(token))
			})
		}
		// Let the waiting requests pile up behind the one fetch.
		time.AfterFunc(20*time.Millisecond, func() { close(gate) })
		wg.Wait()
		return errs
	}
	for i, err := range run(googleToken(t, keys.google, clock.now().Unix()), 32) {
		if err != nil {
			t.Fatalf("cold cache request %d: %v", i, err)
		}
	}
	if got := stub.count(); got != 1 {
		t.Fatalf("cold cache fetches = %d, want 1", got)
	}

	gate = make(chan struct{})
	stub.mu.Lock()
	stub.gate = gate
	stub.mu.Unlock()
	stub.set(googleJWKS, serviceauth.JWKS{Keys: []serviceauth.JWK{keys.google.public(), rotated.public()}})
	clock.advance(time.Hour)
	var wg sync.WaitGroup
	var oldErrs, newErrs []error
	oldToken := googleToken(t, keys.google, clock.now().Unix())
	newToken := googleToken(t, rotated, clock.now().Unix())
	wg.Go(func() { oldErrs = run(oldToken, 16) })
	wg.Go(func() {
		for range 16 {
			_, err := v.Authenticate(request(newToken))
			newErrs = append(newErrs, err)
		}
	})
	wg.Wait()
	for _, err := range append(oldErrs, newErrs...) {
		if err != nil {
			t.Fatalf("request across the refresh: %v", err)
		}
	}
	if got := stub.count(); got != 2 {
		t.Fatalf("fetches = %d, want 2", got)
	}
}

func TestHTTPFetcher(t *testing.T) {
	var gotAuth, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotAccept = r.Header.Get("Authorization"), r.Header.Get("Accept")
		switch r.URL.Path {
		case "/keys":
			_, _ = fmt.Fprint(w, `{"keys":[]}`)
		case "/big":
			_, _ = w.Write([]byte(strings.Repeat(" ", 1<<20+1)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	fetch := serviceauth.HTTPFetcher(srv.Client())

	body, err := fetch(context.Background(), srv.URL+"/keys", "sa-token")
	if err != nil || string(body) != `{"keys":[]}` {
		t.Fatalf("fetch = %q, %v", body, err)
	}
	if gotAuth != "Bearer sa-token" || gotAccept != "application/json" {
		t.Fatalf("headers sent: Authorization %q, Accept %q", gotAuth, gotAccept)
	}
	if _, err := fetch(context.Background(), srv.URL+"/keys", ""); err != nil || gotAuth != "" {
		t.Fatalf("no bearer: Authorization %q, error %v", gotAuth, err)
	}
	if _, err := fetch(context.Background(), srv.URL+"/missing", ""); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("404: error %v", err)
	}
	if _, err := fetch(context.Background(), srv.URL+"/big", ""); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("oversized: error %v", err)
	}
}

func TestThumbprintMatchesRFC8037(t *testing.T) {
	// RFC 8037 appendix A.3.
	got, err := serviceauth.Thumbprint(serviceauth.JWK{Kty: "OKP", Crv: "Ed25519", X: "11qYAYKxCrfVS_7TyWQHOg7hcvPapiMlrwIaaPcHURo"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "kPrK_qmxVWaYVA9wwBF6Iuo3vVzz7TxHCTwXBygrS4k"; got != want {
		t.Fatalf("thumbprint = %s, want %s", got, want)
	}
	if _, err := serviceauth.Thumbprint(serviceauth.JWK{Kty: "oct"}); err == nil {
		t.Fatal("an oct key has no thumbprint here")
	}
}
