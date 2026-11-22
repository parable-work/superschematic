package middleware_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/parable-work/superschematic/runtime/http/go/middleware"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// countingStore is a PrincipalRateLimitStore that counts per (bucket,
// principal) in memory and refuses past limit with retry, or fails with err.
type countingStore struct {
	mu     sync.Mutex
	counts map[[2]string]int
	retry  time.Duration
	err    error
}

func (s *countingStore) Take(_ context.Context, bucket, principal string, limit int, _ time.Duration) (middleware.RateLimitDecision, error) {
	if s.err != nil {
		return middleware.RateLimitDecision{}, s.err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counts == nil {
		s.counts = map[[2]string]int{}
	}
	key := [2]string{bucket, principal}
	s.counts[key]++
	if s.counts[key] > limit {
		return middleware.RateLimitDecision{RetryAfter: s.retry}, nil
	}
	return middleware.RateLimitDecision{Allowed: true}, nil
}

func principalLimited(store middleware.PrincipalRateLimitStore, limit int) http.Handler {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	logger := func(context.Context) *zap.Logger { return zap.NewNop() }
	return middleware.PrincipalRateLimit(store, "POST /api/orders", limit, time.Minute, middleware.SessionPrincipal, logger)(ok)
}

func requestAs(principal string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/orders", nil)
	if principal != "" {
		r = r.WithContext(session.ContextWithPrincipalID(r.Context(), principal))
	}
	return r
}

type problem struct {
	Status int    `json:"status"`
	Detail string `json:"detail"`
	Code   string `json:"code"`
}

func serve(t *testing.T, h http.Handler, r *http.Request) (*httptest.ResponseRecorder, problem) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var p problem
	if w.Code != http.StatusNoContent {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &p), w.Body.String())
	}
	return w, p
}

// TestPrincipalRateLimit (D131): each principal has its own budget; past
// it the route answers 429 with Retry-After in whole seconds, at least 1; a
// request with no principal is refused 400 and counts nothing; a store
// failure answers 503.
func TestPrincipalRateLimit(t *testing.T) {
	t.Run("each principal spends its own budget", func(t *testing.T) {
		store := &countingStore{retry: 1500 * time.Millisecond}
		h := principalLimited(store, 2)
		for range 2 {
			w, _ := serve(t, h, requestAs("ana"))
			assert.Equal(t, http.StatusNoContent, w.Code)
		}
		w, p := serve(t, h, requestAs("ana"))
		assert.Equal(t, http.StatusTooManyRequests, w.Code)
		assert.Equal(t, "2", w.Header().Get("Retry-After"))
		assert.Equal(t, "This principal exceeded 2 requests per minute", p.Detail)
		assert.Equal(t, "too_many_requests", p.Code)
		w, _ = serve(t, h, requestAs("bo"))
		assert.Equal(t, http.StatusNoContent, w.Code)
	})
	t.Run("a refusal never says retry now", func(t *testing.T) {
		h := principalLimited(&countingStore{}, 0)
		w, _ := serve(t, h, requestAs("ana"))
		assert.Equal(t, http.StatusTooManyRequests, w.Code)
		assert.Equal(t, "1", w.Header().Get("Retry-After"))
	})
	t.Run("no principal", func(t *testing.T) {
		store := &countingStore{}
		w, p := serve(t, principalLimited(store, 10), requestAs(""))
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Equal(t, "rate_limit_principal_missing", p.Code)
		assert.Empty(t, store.counts)
	})
	t.Run("store failure", func(t *testing.T) {
		w, p := serve(t, principalLimited(&countingStore{err: errors.New("store down")}, 10), requestAs("ana"))
		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
		assert.Equal(t, "rate_limit_unavailable", p.Code)
	})
	t.Run("a caller that hung up gets no answer", func(t *testing.T) {
		ctx, cancel := context.WithCancel(session.ContextWithPrincipalID(context.Background(), "ana"))
		cancel()
		r := httptest.NewRequest(http.MethodPost, "/api/orders", nil).WithContext(ctx)
		w := httptest.NewRecorder()
		principalLimited(&countingStore{err: context.Canceled}, 10).ServeHTTP(w, r)
		assert.Empty(t, w.Body.String())
	})
}
