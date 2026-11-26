// This file runs inside the generated API module of router-options-api
// (TestRouterOptionsAnswerTheirRequests copies it there). claimWidget
// allows five requests a minute per client: two routers that share one
// Config.ClientRateLimits refuse a client at the sixth request, whichever
// router its requests reach; without it each router counts alone; and a
// store that is unavailable makes each count alone, with no log line.
package routeroptionsapi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	runtimemiddleware "github.com/parable-work/superschematic/runtime/http/go/middleware"

	api "example.com/schemas/api/router-options-api"
)

// sharedCounts is a ClientRateLimitStore in memory that the routers of a
// test share; err, when set, fails every call.
type sharedCounts struct {
	mu     sync.Mutex
	counts map[runtimemiddleware.ClientRateLimitWindow]int
	err    error
}

func (s *sharedCounts) Add(_ context.Context, window runtimemiddleware.ClientRateLimitWindow, n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.counts[window] += n
	return nil
}

func (s *sharedCounts) Counts(_ context.Context, current, previous runtimemiddleware.ClientRateLimitWindow) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, 0, s.err
	}
	return s.counts[current], s.counts[previous], nil
}

func limitedRouter(t *testing.T, limits *runtimemiddleware.ClientRateLimits) http.Handler {
	t.Helper()
	r := chi.NewRouter()
	if err := api.RegisterRoutes(r, api.Config{
		Logger:           zap.NewNop(),
		Implementations:  api.Implementations{Widget: &widgets{}},
		ClientRateLimits: limits,
	}); err != nil {
		t.Fatalf("RegisterRoutes: %v", err)
	}
	return r
}

// claims sends n claims from one client, in turn to each router, and
// returns the statuses.
func claims(routers []http.Handler, n int) string {
	var codes []int
	for i := range n {
		req := httptest.NewRequest(http.MethodPost, "/api/widgets/claim", strings.NewReader(`{"token":"0b7b4c4e-6a43-4d4b-9a8e-1f2d3c4b5a69"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.7:1234"
		rec := httptest.NewRecorder()
		routers[i%len(routers)].ServeHTTP(rec, req)
		codes = append(codes, rec.Code)
	}
	return fmt.Sprint(codes)
}

func TestClientRateLimitsAcrossRouters(t *testing.T) {
	store := &sharedCounts{counts: map[runtimemiddleware.ClientRateLimitWindow]int{}}
	limits, err := runtimemiddleware.NewClientRateLimits(store, []byte("a secret of sixteen bytes"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := claims([]http.Handler{limitedRouter(t, limits), limitedRouter(t, limits)}, 7), "[200 200 200 200 200 429 429]"; got != want {
		t.Errorf("two routers over one store: %s, want %s", got, want)
	}
	if got, want := claims([]http.Handler{limitedRouter(t, nil), limitedRouter(t, nil)}, 7), "[200 200 200 200 200 200 200]"; got != want {
		t.Errorf("two routers without a store: %s, want %s", got, want)
	}

	core, logs := observer.New(zap.DebugLevel)
	down := &sharedCounts{err: fmt.Errorf("store disabled: %w", runtimemiddleware.ErrClientRateLimitStoreUnavailable)}
	downLimits, err := runtimemiddleware.NewClientRateLimits(down, []byte("a secret of sixteen bytes"), zap.New(core))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := claims([]http.Handler{limitedRouter(t, downLimits), limitedRouter(t, downLimits)}, 12), "[200 200 200 200 200 200 200 200 200 200 429 429]"; got != want {
		t.Errorf("two routers over a store that is down: %s, want %s", got, want)
	}
	if logs.Len() != 0 {
		t.Errorf("a store that is down logged %d lines", logs.Len())
	}
}
