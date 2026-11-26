package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// memoryClientStore is a ClientRateLimitStore in memory, which the replicas
// of a test share. err, when set, fails every call.
type memoryClientStore struct {
	mu     sync.Mutex
	counts map[ClientRateLimitWindow]int
	err    error
	keys   []string
}

func newMemoryClientStore() *memoryClientStore {
	return &memoryClientStore{counts: map[ClientRateLimitWindow]int{}}
}

func (s *memoryClientStore) Add(_ context.Context, window ClientRateLimitWindow, n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.counts[window] += n
	s.keys = append(s.keys, window.Limiter+" "+window.Client)
	return nil
}

func (s *memoryClientStore) Counts(_ context.Context, current, previous ClientRateLimitWindow) (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, 0, s.err
	}
	return s.counts[current], s.counts[previous], nil
}

// replica is one replica's route: a per-client RateLimit of limit requests
// a minute over limits, as the generated router mounts it.
func replica(t *testing.T, limits *ClientRateLimits, limit int, logger *zap.Logger) http.Handler {
	t.Helper()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	getLogger := func(context.Context) *zap.Logger { return logger }
	return RateLimit(limit, time.Minute, getLogger, WithClientRateLimits(limits, "POST /api/sessions"))(ok)
}

func call(handler http.Handler, ip string) int {
	r := httptest.NewRequest(http.MethodPost, "/api/sessions", nil)
	r.RemoteAddr = ip + ":1234"
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w.Code
}

// TestClientRateLimitsShareOneCount: two replicas over one store refuse a
// client at the limit's count, whichever replica its requests reach, and
// count another client apart. The store sees the limiter and a hash of the
// client, never its address.
func TestClientRateLimitsShareOneCount(t *testing.T) {
	store := newMemoryClientStore()
	limits, err := NewClientRateLimits(store, []byte("a secret of sixteen bytes"), nil)
	if err != nil {
		t.Fatal(err)
	}
	replicas := []http.Handler{replica(t, limits, 3, zap.NewNop()), replica(t, limits, 3, zap.NewNop())}
	var got []int
	for i := range 5 {
		got = append(got, call(replicas[i%2], "203.0.113.7"))
	}
	if want := fmt.Sprint([]int{204, 204, 204, 429, 429}); fmt.Sprint(got) != want {
		t.Errorf("answers %v, want %s", got, want)
	}
	if code := call(replicas[1], "203.0.113.8"); code != http.StatusNoContent {
		t.Errorf("another client: %d", code)
	}
	for _, key := range store.keys {
		if key[:len("POST /api/sessions ")] != "POST /api/sessions " || len(key) != len("POST /api/sessions ")+2*clientRateLimitHashBytes {
			t.Errorf("store key %q is not the limiter and a client hash", key)
		}
	}

	// Without limits each replica counts alone: the default.
	alone := []http.Handler{replica(t, nil, 3, zap.NewNop()), replica(t, nil, 3, zap.NewNop())}
	got = nil
	for i := range 6 {
		got = append(got, call(alone[i%2], "203.0.113.7"))
	}
	if want := fmt.Sprint([]int{204, 204, 204, 204, 204, 204}); fmt.Sprint(got) != want {
		t.Errorf("replicas without a store: %v, want %s", got, want)
	}
}

// TestClientRateLimitsFallBackWithoutALogLine: a store that is unavailable
// makes each replica count alone, with no log line; any other store error
// makes it count alone too, and logs once.
func TestClientRateLimitsFallBackWithoutALogLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		logs int
	}{
		{"unavailable", fmt.Errorf("memory store disabled: %w", ErrClientRateLimitStoreUnavailable), 0},
		{"failed", errors.New("connection reset"), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.DebugLevel)
			store := newMemoryClientStore()
			store.err = tc.err
			limits, err := NewClientRateLimits(store, []byte("a secret of sixteen bytes"), zap.New(core))
			if err != nil {
				t.Fatal(err)
			}
			replicas := []http.Handler{replica(t, limits, 2, zap.NewNop()), replica(t, limits, 2, zap.NewNop())}
			var got []int
			for i := range 6 {
				got = append(got, call(replicas[i%2], "203.0.113.7"))
			}
			if want := fmt.Sprint([]int{204, 204, 204, 204, 429, 429}); fmt.Sprint(got) != want {
				t.Errorf("answers %v, want %s: each replica counts alone", got, want)
			}
			if n := logs.Len(); n != tc.logs {
				t.Errorf("%d log lines, want %d", n, tc.logs)
			}
		})
	}
}

// TestNewClientRateLimitsRefusesAWeakSetup: no store, or a short secret.
func TestNewClientRateLimitsRefusesAWeakSetup(t *testing.T) {
	if _, err := NewClientRateLimits(nil, []byte("a secret of sixteen bytes"), nil); err == nil {
		t.Error("a nil store was taken")
	}
	if _, err := NewClientRateLimits(newMemoryClientStore(), []byte("short"), nil); err == nil {
		t.Error("a short secret was taken")
	}
	if (*ClientRateLimits)(nil).Counter("x") != nil {
		t.Error("a nil ClientRateLimits has a counter")
	}
}
