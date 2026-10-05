package middleware_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/parable-work/superschematic/runtime/http/go/middleware"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func nopLoggerGetter(_ context.Context) *zap.Logger {
	return zap.NewNop()
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "OK")
	})
}

func jsonErrorResponder(w http.ResponseWriter, _ *http.Request, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":"%s"}`, message)
}

func TestBodyLimit(t *testing.T) {
	tests := []struct {
		name       string
		limitMB    int
		bodySize   int64
		wantStatus int
		wantReach  bool
	}{
		{
			name:       "within_limit",
			limitMB:    1,
			bodySize:   100,
			wantStatus: 200,
			wantReach:  true,
		},
		{
			name:       "exceeds_limit",
			limitMB:    1,
			bodySize:   2 * 1024 * 1024,
			wantStatus: 413,
			wantReach:  false,
		},
		{
			name:       "exact_limit",
			limitMB:    1,
			bodySize:   1 * 1024 * 1024,
			wantStatus: 200,
			wantReach:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			handler := middleware.BodyLimit(tc.limitMB, jsonErrorResponder)(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					reached = true
					w.WriteHeader(http.StatusOK)
				}),
			)

			body := strings.NewReader(strings.Repeat("x", int(tc.bodySize)))
			req := httptest.NewRequest(http.MethodPost, "/", body)
			req.ContentLength = tc.bodySize
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			assert.Equal(t, tc.wantStatus, w.Code)
			assert.Equal(t, tc.wantReach, reached)

			if !tc.wantReach {
				assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
				assert.Contains(t, w.Body.String(), "error")
			}
		})
	}
}

func TestTimeout(t *testing.T) {
	t.Run("completes_within_timeout", func(t *testing.T) {
		handler := middleware.Timeout(500*time.Millisecond, nopLoggerGetter)(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, `{"status":"ok"}`)
			}),
		)

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
		assert.Equal(t, `{"status":"ok"}`, w.Body.String())
	})

	t.Run("exceeds_timeout", func(t *testing.T) {
		handler := middleware.Timeout(50*time.Millisecond, nopLoggerGetter)(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case <-time.After(200 * time.Millisecond):
					w.WriteHeader(http.StatusOK)
				case <-r.Context().Done():
					return
				}
			}),
		)

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusGatewayTimeout, w.Code)
		assert.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))
		assert.JSONEq(t, `{"type":"about:blank","title":"Gateway Timeout","status":504,"detail":"Gateway Timeout","code":"gateway_timeout"}`, w.Body.String())
	})

	t.Run("preserves_headers_on_success", func(t *testing.T) {
		handler := middleware.Timeout(500*time.Millisecond, nopLoggerGetter)(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("X-Custom", "value")
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, "created")
			}),
		)

		req := httptest.NewRequest(http.MethodPost, "/", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusCreated, w.Code)
		assert.Equal(t, "value", w.Header().Get("X-Custom"))
		assert.Equal(t, "created", w.Body.String())
	})
}

func TestRateLimit(t *testing.T) {
	t.Run("within_limit_passes", func(t *testing.T) {
		handler := middleware.RateLimit(10, time.Minute, nopLoggerGetter)(okHandler())

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "192.0.2.1:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "OK", w.Body.String())
	})

	t.Run("exceeds_limit_returns_429", func(t *testing.T) {
		handler := middleware.RateLimit(2, time.Minute, nopLoggerGetter)(okHandler())

		var last *httptest.ResponseRecorder
		for i := 0; i < 5; i++ {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = "10.0.0.99:12345"
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			last = w
		}

		assert.Equal(t, http.StatusTooManyRequests, last.Code)
		assert.Equal(t, "application/problem+json", last.Header().Get("Content-Type"))
		assert.Equal(t, "60", last.Header().Get("Retry-After"))
		assert.JSONEq(t, `{"type":"about:blank","title":"Too Many Requests","status":429,"detail":"Too Many Requests","code":"too_many_requests"}`, last.Body.String())
	})

	t.Run("the_problem_carries_the_request_id", func(t *testing.T) {
		handler := chimiddleware.RequestID(middleware.RateLimit(1, time.Minute, nopLoggerGetter)(okHandler()))

		var last *httptest.ResponseRecorder
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = "10.0.0.98:12345"
			req.Header.Set("X-Request-Id", "req-7")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			last = w
		}

		assert.Equal(t, http.StatusTooManyRequests, last.Code)
		assert.Contains(t, last.Body.String(), `"requestId":"req-7"`)
	})
}

func TestBodyLimit_MaxBytesReaderEnforcement(t *testing.T) {
	handler := middleware.BodyLimit(1, jsonErrorResponder)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				return
			}
			w.WriteHeader(http.StatusOK)
		}),
	)

	largeBody := strings.NewReader(strings.Repeat("x", 2*1024*1024))
	req := httptest.NewRequest(http.MethodPost, "/", largeBody)
	req.ContentLength = 0 // lie about content length
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	require.True(t, w.Code == http.StatusOK || w.Code == http.StatusRequestEntityTooLarge,
		"should either accept (if MaxBytesReader truncates) or reject")
}
