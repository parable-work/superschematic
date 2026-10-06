package serviceauth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/parable-work/superschematic/runtime/http/go/serviceauth"
)

// metadataServer is a stand-in for the GCE metadata server that issues
// unsigned ID tokens expiring an hour after the clock, and counts them.
type metadataServer struct {
	clock     *fakeClock
	issued    atomic.Int32
	status    atomic.Int32
	audiences sync.Map
}

func (m *metadataServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/computeMetadata/v1/instance/service-accounts/default/identity" || r.Header.Get("Metadata-Flavor") != "Google" {
		http.Error(w, "not the identity endpoint", http.StatusNotFound)
		return
	}
	if status := m.status.Load(); status != 0 {
		http.Error(w, "metadata unavailable", int(status))
		return
	}
	audience := r.URL.Query().Get("audience")
	m.audiences.Store(audience, true)
	n := m.issued.Add(1)
	claims := fmt.Sprintf(`{"aud":%q,"exp":%d,"n":%d}`, audience, m.clock.now().Unix()+3600, n)
	_, _ = fmt.Fprintf(w, "%s.%s.sig\n", b64([]byte(`{"alg":"RS256"}`)), b64([]byte(claims)))
}

func TestGoogleIDToken(t *testing.T) {
	clock := newClock(parityNow)
	meta := &metadataServer{clock: clock}
	srv := httptest.NewServer(meta)
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GCE_METADATA_HOST", u.Host)
	const audience = "https://shop-api-abc.a.run.app/?x=1&y=2"
	source := serviceauth.GoogleIDToken(audience, serviceauth.WithClock(clock.now), serviceauth.WithHTTPClient(srv.Client()))
	ctx := context.Background()

	first, err := source(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(first, "\n") || strings.Count(first, ".") != 2 {
		t.Fatalf("token %q", first)
	}
	if _, ok := meta.audiences.Load(audience); !ok {
		t.Fatal("the audience must reach the metadata server whole, query-escaped")
	}

	// Cached until five minutes before exp.
	clock.advance(54 * time.Minute)
	if again, _ := source(ctx, false); again != first || meta.issued.Load() != 1 {
		t.Fatalf("within the hour: fetched %d tokens", meta.issued.Load())
	}
	clock.advance(time.Minute)
	second, err := source(ctx, false)
	if err != nil || second == first || meta.issued.Load() != 2 {
		t.Fatalf("five minutes before exp: %v, fetched %d tokens", err, meta.issued.Load())
	}

	// fresh fetches whatever is cached.
	third, err := source(ctx, true)
	if err != nil || third == second || meta.issued.Load() != 3 {
		t.Fatalf("fresh: %v, fetched %d tokens", err, meta.issued.Load())
	}

	// Concurrent callers share one fetch.
	clock.advance(2 * time.Hour)
	var wg sync.WaitGroup
	tokens := make([]string, 16)
	for i := range tokens {
		wg.Go(func() { tokens[i], _ = source(ctx, false) })
	}
	wg.Wait()
	for _, tok := range tokens {
		if tok != tokens[0] || tok == "" {
			t.Fatalf("concurrent callers got different tokens: %q", tokens)
		}
	}
	if got := meta.issued.Load(); got != 4 {
		t.Fatalf("concurrent fetches = %d, want 1 more than 3", got)
	}

	meta.status.Store(http.StatusInternalServerError)
	if _, err := source(ctx, true); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("a refusing metadata server: error %v", err)
	}
}

func TestGoogleIDTokenRefusesATokenWithoutExp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, "%s.%s.sig", b64([]byte(`{}`)), b64([]byte(`{"aud":"x"}`)))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	t.Setenv("GCE_METADATA_HOST", u.Host)
	if _, err := serviceauth.GoogleIDToken("x", serviceauth.WithHTTPClient(srv.Client()))(context.Background(), false); err == nil || !strings.Contains(err.Error(), "no exp") {
		t.Fatalf("error %v", err)
	}
}

func TestTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	clock := newClock(parityNow)
	source := serviceauth.TokenFile(path, serviceauth.WithClock(clock.now))
	ctx := context.Background()

	if _, err := source(ctx, false); err == nil {
		t.Fatal("a missing file is an error")
	}
	write("  \n")
	if _, err := source(ctx, false); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("an empty file: error %v", err)
	}
	write("token-1\n")
	if got, err := source(ctx, false); got != "token-1" || err != nil {
		t.Fatalf("read %q, %v", got, err)
	}
	// The kubelet replaces the file; it is read again a minute later.
	write("token-2")
	clock.advance(59 * time.Second)
	if got, _ := source(ctx, false); got != "token-1" {
		t.Fatalf("within the minute: %q", got)
	}
	clock.advance(time.Second)
	if got, _ := source(ctx, false); got != "token-2" {
		t.Fatalf("after the minute: %q", got)
	}
	write("token-3")
	if got, _ := source(ctx, true); got != "token-3" {
		t.Fatalf("fresh: %q", got)
	}
}

func TestSignedTokenIsTheGenericConnectorsToken(t *testing.T) {
	keys := newParityKeys(t)
	clock := newClock(parityNow)
	jti := make([]byte, 48) // three tokens' worth
	for i := range jti {
		jti[i] = byte(i)
	}
	source, err := serviceauth.SignedToken(keys.edgeOrders.privateJWK(t), "orders", "orders", "shop-api",
		serviceauth.WithClock(clock.now), serviceauth.WithRandom(bytes.NewReader(jti)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	token, err := source(ctx, false)
	if err != nil {
		t.Fatal(err)
	}

	// Byte for byte: the header and claims in the documented order.
	claims := object{{"iss", "orders"}, {"sub", "orders"}, {"aud", "shop-api"}, {"iat", parityNow}, {"exp", parityNow + 300}, {"jti", b64(jti[:16])}}
	if want := keys.edgeOrders.jwt(t, serviceauth.AlgEdDSA, keys.edgeOrders.header(serviceauth.AlgEdDSA), claims); token != want {
		t.Fatalf("token\n%s\nwant\n%s", token, want)
	}
	if header := string(unb64(t, segment(token, 0))); header != `{"alg":"EdDSA","kid":"`+keys.edgeOrders.kid+`","typ":"JWT"}` {
		t.Fatalf("header %s", header)
	}

	// The keypair config verifies it.
	v, err := serviceauth.New(parityConfigs(keys)["keypair"], serviceauth.WithClock(clock.now))
	if err != nil {
		t.Fatal(err)
	}
	caller, err := v.Authenticate(request(token))
	if err != nil || caller.Deployable != "orders" {
		t.Fatalf("verify: %+v, %v", caller, err)
	}

	// Kept while a minute or more is left, signed again under it.
	clock.advance(240 * time.Second)
	if again, _ := source(ctx, false); again != token {
		t.Fatal("a token with 60 seconds left is kept")
	}
	clock.advance(time.Second)
	renewed, _ := source(ctx, false)
	if renewed == token {
		t.Fatal("a token with under 60 seconds left is signed again")
	}
	var renewedClaims struct {
		Iat, Exp int64
		Jti      string
	}
	if err := json.Unmarshal(unb64(t, segment(renewed, 1)), &renewedClaims); err != nil {
		t.Fatal(err)
	}
	if renewedClaims.Iat != parityNow+241 || renewedClaims.Exp != parityNow+541 || renewedClaims.Jti != b64(jti[16:32]) {
		t.Fatalf("renewed claims %+v", renewedClaims)
	}
	if fresh, _ := source(ctx, true); fresh == renewed {
		t.Fatal("fresh signs again")
	}
	if _, err := source(ctx, true); err == nil {
		t.Fatal("a random source that runs dry is an error")
	}
}

func TestSignedTokenIsSafeForConcurrentUse(t *testing.T) {
	keys := newParityKeys(t)
	source, err := serviceauth.SignedToken(keys.edgeOrders.privateJWK(t), "orders", "orders", "shop-api")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	tokens := make([]string, 16)
	for i := range tokens {
		wg.Go(func() { tokens[i], _ = source(context.Background(), i%4 == 0) })
	}
	wg.Wait()
	for i, tok := range tokens {
		if strings.Count(tok, ".") != 2 {
			t.Fatalf("token %d = %q", i, tok)
		}
	}
}

func TestSignedTokenRefusesKeysItCannotSignWith(t *testing.T) {
	keys := newParityKeys(t)
	good := keys.edgeOrders.public()
	good.D = b64(keys.edgeOrders.ed.Seed())
	mutate := func(f func(*serviceauth.JWK)) []byte {
		j := good
		f(&j)
		data, _ := json.Marshal(j)
		return data
	}
	cases := []struct {
		name string
		jwk  []byte
		want string
	}{
		{"not JSON", []byte("{"), "signing key"},
		{"an RSA key", mutate(func(j *serviceauth.JWK) { j.Kty = "RSA" }), "not OKP Ed25519"},
		{"another curve", mutate(func(j *serviceauth.JWK) { j.Crv = "X25519" }), "not OKP Ed25519"},
		{"no kid", mutate(func(j *serviceauth.JWK) { j.Kid = "" }), "no kid"},
		{"no d", mutate(func(j *serviceauth.JWK) { j.D = "" }), "no d"},
		{"a short d", mutate(func(j *serviceauth.JWK) { j.D = b64([]byte("short")) }), "not 32 bytes"},
		{"x of another key", mutate(func(j *serviceauth.JWK) { j.X = keys.edgeReports.public().X }), "not the public key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := serviceauth.SignedToken(tc.jwk, "orders", "orders", "shop-api"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want one containing %q", err, tc.want)
			}
		})
	}
}
