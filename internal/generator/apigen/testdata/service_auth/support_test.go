// This file runs inside the generated API module of
// fixture-service-auth-api, beside routes_test.go or public_routes_test.go
// (TestServiceAuthRoutes and TestServiceAuthPublicRoutes copy them there).
// It holds what both share: a service authenticator over two key-pair
// callers, their tokens, an implementation that records what each handler
// saw, and a request helper.
package fixtureserviceauthapi_test

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/parable-work/superschematic/runtime/http/go/serviceauth"
	runtimesession "github.com/parable-work/superschematic/runtime/http/go/session"

	types "example.com/schemas/types/go/fixture-service-auth-api"
)

// audience is the callee's name in the callers' tokens.
const audience = "fixture-service-auth-api"

// edgeKey is the test key both callers sign with.
var edgeKey = ed25519.NewKeyFromSeed(func() []byte {
	sum := sha256.Sum256([]byte("fixture-service-auth-api test key"))
	return sum[:]
}())

func edgeJWK(t *testing.T) serviceauth.JWK {
	t.Helper()
	j := serviceauth.JWK{Kty: "OKP", Crv: "Ed25519", X: base64.RawURLEncoding.EncodeToString(edgeKey.Public().(ed25519.PublicKey))}
	kid, err := serviceauth.Thumbprint(j)
	if err != nil {
		t.Fatal(err)
	}
	j.Kid = kid
	return j
}

// authenticator admits two callers: orders, the server of the API the
// fixture's from lists, and billing, which serves another API.
func authenticator(t *testing.T) serviceauth.Authenticator {
	t.Helper()
	key := edgeJWK(t)
	entry := func(deployable, serves string) serviceauth.IssuerConfig {
		return serviceauth.IssuerConfig{
			Issuer:             deployable,
			Audience:           audience,
			Algorithms:         []string{serviceauth.AlgEdDSA},
			Keys:               []serviceauth.JWK{key},
			MaxLifetimeSeconds: 300,
			Callers: map[string]serviceauth.CallerConfig{
				deployable: {Deployable: deployable, Serves: []string{serves}},
			},
		}
	}
	v, err := serviceauth.New(serviceauth.Config{Issuers: []serviceauth.IssuerConfig{
		entry("orders", "fixture-service-caller-api"),
		entry("billing", "billing-api"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// serviceToken signs a token for deployable, as its SignedToken source
// does.
func serviceToken(t *testing.T, deployable string) string {
	t.Helper()
	j := edgeJWK(t)
	j.D = base64.RawURLEncoding.EncodeToString(edgeKey.Seed())
	private, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	source, err := serviceauth.SignedToken(private, deployable, deployable, audience)
	if err != nil {
		t.Fatal(err)
	}
	token, err := source(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// A user's bearer token is "user:" and the permissions it grants, comma
// separated.
const userPrefix = "user:"

// userFrom reads the request's end user from its token, as an auth
// middleware resolves a session; ok is false when there is none.
func userFrom(r *http.Request) (context.Context, bool) {
	token, ok := runtimesession.BearerToken(r.Header)
	if !ok || !strings.HasPrefix(token, userPrefix) {
		return r.Context(), false
	}
	ctx := runtimesession.ContextWithPrincipalID(r.Context(), token)
	ctx = runtimesession.ContextWithRoles(ctx, []runtimesession.Role{{
		ID:          "role-1",
		Name:        "test",
		Permissions: strings.Split(strings.TrimPrefix(token, userPrefix), ","),
	}})
	return ctx, true
}

// seen is what a handler found on its request.
type seen struct {
	caller    string // the service caller's deployable, or ""
	principal string // the end user's principal id, or ""
	forwarded string // serviceauth.ForwardedToken
}

// recorder implements every namespace and records the last call.
type recorder struct {
	mu   sync.Mutex
	last *seen
}

func (rec *recorder) record(ctx context.Context) {
	s := &seen{principal: runtimesession.GetPrincipalID(ctx)}
	if c, ok := serviceauth.CallerFromContext(ctx); ok {
		s.caller = c.Deployable
	}
	s.forwarded, _ = serviceauth.ForwardedToken(ctx)
	rec.mu.Lock()
	rec.last = s
	rec.mu.Unlock()
}

// take returns the last call's record and clears it; nil when no handler
// ran.
func (rec *recorder) take() *seen {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	s := rec.last
	rec.last = nil
	return s
}

func (rec *recorder) ListReservations(ctx context.Context) ([]types.Reservation, error) {
	rec.record(ctx)
	return []types.Reservation{}, nil
}

func (rec *recorder) ReindexStock(ctx context.Context) (*types.StockRun, error) {
	rec.record(ctx)
	return &types.StockRun{Id: "run-1"}, nil
}

func (rec *recorder) ReserveStock(ctx context.Context, sku string) (*types.Reservation, error) {
	rec.record(ctx)
	return &types.Reservation{Id: "res-1", Sku: sku, Held: true}, nil
}

func (rec *recorder) GetReservation(ctx context.Context, id string) (*types.Reservation, error) {
	rec.record(ctx)
	return &types.Reservation{Id: id}, nil
}

func (rec *recorder) ReleaseReservation(ctx context.Context, id string) (*types.Reservation, error) {
	rec.record(ctx)
	return &types.Reservation{Id: id}, nil
}

func (rec *recorder) SyncStatus(ctx context.Context) (*types.StockRun, error) {
	rec.record(ctx)
	return &types.StockRun{Id: "status"}, nil
}

func (rec *recorder) SyncStock(ctx context.Context) (*types.StockRun, error) {
	rec.record(ctx)
	return &types.StockRun{Id: "sync"}, nil
}

func (rec *recorder) SyncMyStock(ctx context.Context) (*types.StockRun, error) {
	rec.record(ctx)
	return &types.StockRun{Id: "mine"}, nil
}

// credentials are a request's headers: a service token, a user token and
// any other header, each set when not empty.
type credentials struct {
	service string
	user    string
	extra   map[string]string
}

// do sends a request and returns its status and problem code.
func do(t *testing.T, base, method, path string, creds credentials, body io.Reader) (int, string) {
	t.Helper()
	if body == nil && method == http.MethodPost {
		body = strings.NewReader(`{"sku":"sku-1"}`)
	}
	req, err := http.NewRequest(method, base+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if creds.service != "" {
		req.Header.Set("Service-Authorization", "Bearer "+creds.service)
	}
	if creds.user != "" {
		req.Header.Set("Authorization", "Bearer "+creds.user)
	}
	for name, value := range creds.extra {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var problem struct {
		Code string `json:"code"`
	}
	if resp.StatusCode >= 400 {
		_ = json.Unmarshal(data, &problem)
	}
	return resp.StatusCode, problem.Code
}

// check is one request and what must come of it.
type check struct {
	name   string
	method string
	path   string
	creds  credentials
	status int
	code   string
	seen   *seen // nil: the handler must not run
}

func run(t *testing.T, base string, rec *recorder, checks []check) {
	t.Helper()
	for _, c := range checks {
		rec.take()
		status, code := do(t, base, c.method, c.path, c.creds, nil)
		if status != c.status || code != c.code {
			t.Errorf("%s: got %d %q, want %d %q", c.name, status, code, c.status, c.code)
			continue
		}
		got := rec.take()
		switch {
		case c.seen == nil && got != nil:
			t.Errorf("%s: the handler ran: %+v", c.name, *got)
		case c.seen != nil && got == nil:
			t.Errorf("%s: the handler did not run", c.name)
		case c.seen != nil && *got != *c.seen:
			t.Errorf("%s: the handler saw %+v, want %+v", c.name, *got, *c.seen)
		}
	}
}
