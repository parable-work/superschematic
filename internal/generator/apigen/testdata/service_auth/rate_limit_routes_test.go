// This file runs inside the generated API module of
// fixture-service-auth-api generated as a public API over fixture-db, with
// three @rateLimit routes (TestPrincipalRateLimitRoutes copies it there):
// getReservation counts per principal (key 'principal'), releaseReservation
// per the organization an extension's key reads from X-Org (key 'org'),
// and syncStatus per client IP (key 'ip'). Each principal route runs after
// the AuthMiddleware and the permission check, counts in
// Config.PrincipalRateLimitStore, and refuses a request with no principal
// 400 and a store failure 503 (D131).
package fixtureserviceauthapi_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/parable-work/superschematic/runtime/http/go/middleware"
	"github.com/parable-work/superschematic/runtime/http/go/response"
	"go.uber.org/zap"

	api "example.com/schemas/api/fixture-service-auth-api"
	orm "example.com/schemas/orm/fixture-db"
)

// limitDatabase stands in for the ORM; no route here reads it.
type limitDatabase struct{ orm.DatabaseInterface }

// windowStore counts per (bucket, principal) and records each Take; fail
// makes every Take fail.
type windowStore struct {
	mu     sync.Mutex
	counts map[[2]string]int
	takes  []string
	fail   bool
}

func (s *windowStore) Take(_ context.Context, bucket, principal string, limit int, window time.Duration) (middleware.RateLimitDecision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.takes = append(s.takes, bucket+" "+principal)
	if s.fail {
		return middleware.RateLimitDecision{}, errors.New("store down")
	}
	if window != time.Minute {
		return middleware.RateLimitDecision{}, errors.New("unexpected window " + window.String())
	}
	if s.counts == nil {
		s.counts = map[[2]string]int{}
	}
	key := [2]string{bucket, principal}
	s.counts[key]++
	if s.counts[key] > limit {
		return middleware.RateLimitDecision{RetryAfter: 30 * time.Second}, nil
	}
	return middleware.RateLimitDecision{Allowed: true}, nil
}

func (s *windowStore) taken() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.takes...)
}

func serveLimited(t *testing.T, store *windowStore) string {
	t.Helper()
	config := func(store middleware.PrincipalRateLimitStore) api.Config {
		return api.Config{
			DB:     limitDatabase{},
			Logger: zap.NewNop(),
			AuthMiddleware: func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					ctx, ok := userFrom(r)
					if !ok {
						response.Error(w, http.StatusUnauthorized, "Authentication required", "unauthorized")
						return
					}
					next.ServeHTTP(w, r.WithContext(ctx))
				})
			},
			ServiceAuthenticator:    authenticator(t),
			PrincipalRateLimitStore: store,
			Implementations:         api.Implementations{Ledger: &recorder{}, Stock: &recorder{}, Sync: &recorder{}},
		}
	}
	err := api.RegisterRoutes(chi.NewRouter(), config(nil))
	if err == nil || !strings.Contains(err.Error(), "PrincipalRateLimitStore") {
		t.Fatalf("RegisterRoutes without a store: err = %v, want a PrincipalRateLimitStore error", err)
	}
	router := chi.NewRouter()
	if err := api.RegisterRoutes(router, config(store)); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server.URL
}

// answer is what a request got: its status, problem code and detail, and
// Retry-After.
type answer struct {
	status     int
	code       string
	retryAfter string
	body       string
}

func send(t *testing.T, base, method, path string, headers map[string]string) answer {
	t.Helper()
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(`{}`)
	}
	req, err := http.NewRequest(method, base+path, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	code := ""
	if resp.StatusCode >= 400 {
		if i := strings.Index(string(data), `"code":"`); i >= 0 {
			rest := string(data)[i+len(`"code":"`):]
			code = rest[:strings.IndexByte(rest, '"')]
		}
	}
	return answer{status: resp.StatusCode, code: code, retryAfter: resp.Header.Get("Retry-After"), body: string(data)}
}

func user(token string, extra ...string) map[string]string {
	h := map[string]string{"Authorization": "Bearer " + token}
	for i := 0; i+1 < len(extra); i += 2 {
		h[extra[i]] = extra[i+1]
	}
	return h
}

func TestPrincipalRateLimitRoutes(t *testing.T) {
	store := &windowStore{}
	base := serveLimited(t, store)
	const ana, bo = "user:stock.write", "user:stock.write,stock.read"

	t.Run("principal: each user spends a budget of two", func(t *testing.T) {
		for range 2 {
			if a := send(t, base, "GET", "/api/stock/reservations/r1", user(ana)); a.status != 200 {
				t.Fatalf("ana within the limit: %+v", a)
			}
		}
		a := send(t, base, "GET", "/api/stock/reservations/r1", user(ana))
		if a.status != 429 || a.code != "too_many_requests" || a.retryAfter != "30" || !strings.Contains(a.body, "exceeded 2 requests per minute") {
			t.Fatalf("ana past the limit: %+v", a)
		}
		if a := send(t, base, "GET", "/api/stock/reservations/r1", user(bo)); a.status != 200 {
			t.Fatalf("bo has a budget of his own: %+v", a)
		}
	})

	t.Run("principal: the AuthMiddleware refuses a request with no user before the limit counts", func(t *testing.T) {
		before := len(store.taken())
		if a := send(t, base, "GET", "/api/stock/reservations/r1", nil); a.status != 401 {
			t.Fatalf("no user: %+v", a)
		}
		if after := len(store.taken()); after != before {
			t.Fatalf("the limit counted a refused request: %d takes, want %d", after, before)
		}
	})

	t.Run("extension key: counts per the principal its reader returns", func(t *testing.T) {
		if a := send(t, base, "POST", "/api/stock/reservations/r1/release", user(ana, "X-Org", "acme")); a.status != 200 {
			t.Fatalf("acme within the limit: %+v", a)
		}
		if a := send(t, base, "POST", "/api/stock/reservations/r1/release", user(bo, "X-Org", "acme")); a.status != 429 {
			t.Fatalf("acme past the limit, another user: %+v", a)
		}
		if a := send(t, base, "POST", "/api/stock/reservations/r1/release", user(ana, "X-Org", "globex")); a.status != 200 {
			t.Fatalf("globex within the limit: %+v", a)
		}
	})

	t.Run("extension key: a request with no principal is refused 400", func(t *testing.T) {
		if a := send(t, base, "POST", "/api/stock/reservations/r1/release", user(ana)); a.status != 400 || a.code != "rate_limit_principal_missing" {
			t.Fatalf("no organization: %+v", a)
		}
	})

	t.Run("ip: counts per client in process, not in the store", func(t *testing.T) {
		before := len(store.taken())
		if a := send(t, base, "GET", "/api/sync/status", nil); a.status != 200 {
			t.Fatalf("first status: %+v", a)
		}
		if a := send(t, base, "GET", "/api/sync/status", nil); a.status != 429 || a.code != "too_many_requests" {
			t.Fatalf("second status: %+v", a)
		}
		if after := len(store.taken()); after != before {
			t.Fatalf("the ip limit counted in the principal store")
		}
	})

	t.Run("extension key: a list route of an Authenticated set", func(t *testing.T) {
		if a := send(t, base, "GET", "/api/ledger/reservations", user(ana, "X-Org", "acme")); a.status != 200 {
			t.Fatalf("acme within the limit: %+v", a)
		}
		if a := send(t, base, "GET", "/api/ledger/reservations", user(ana, "X-Org", "acme")); a.status != 429 {
			t.Fatalf("acme past the limit: %+v", a)
		}
	})

	t.Run("extension key: the body limit refuses before the limit counts", func(t *testing.T) {
		before := len(store.taken())
		big := strings.Repeat("x", 2<<20)
		req, err := http.NewRequest("POST", base+"/api/sync/stock/mine", strings.NewReader(`{"pad":"`+big+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+ana)
		req.Header.Set("X-Org", "acme")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 413 {
			t.Fatalf("a body past the limit: %d", resp.StatusCode)
		}
		if after := len(store.taken()); after != before {
			t.Fatalf("the limit counted a request the body limit refused")
		}
		if a := send(t, base, "POST", "/api/sync/stock/mine", user(ana, "X-Org", "acme")); a.status != 200 {
			t.Fatalf("acme within the limit: %+v", a)
		}
		if a := send(t, base, "POST", "/api/sync/stock/mine", user(ana, "X-Org", "acme")); a.status != 429 {
			t.Fatalf("acme past the limit: %+v", a)
		}
	})

	takes := store.taken()
	wantBuckets := map[string]bool{
		"GET /api/ledger/reservations acme":                true,
		"POST /api/sync/stock/mine acme":                   true,
		"GET /api/stock/reservations/{id} " + ana:          true,
		"GET /api/stock/reservations/{id} " + bo:           true,
		"POST /api/stock/reservations/{id}/release acme":   true,
		"POST /api/stock/reservations/{id}/release globex": true,
	}
	for _, take := range takes {
		if !wantBuckets[take] {
			t.Errorf("unexpected take %q", take)
		}
	}

	t.Run("a store failure answers 503", func(t *testing.T) {
		store.mu.Lock()
		store.fail = true
		store.mu.Unlock()
		if a := send(t, base, "GET", "/api/stock/reservations/r1", user(bo)); a.status != 503 || a.code != "rate_limit_unavailable" {
			t.Fatalf("store down: %+v", a)
		}
	})
}
