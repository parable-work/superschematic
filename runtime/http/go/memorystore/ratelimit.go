package memorystore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/parable-work/superschematic/ir"
)

// The shared rate limit (D75). An API whose config names a rateLimitStore
// counts each @rateLimit route in that store, so every replica spends one
// budget. The rule is httprate's sliding window, which the route's
// in-process limiter applies: the previous window's count weighted by the
// part of it still in the window, plus this window's, and a request is
// allowed while that estimate, rounded, plus one is at most the limit.
// RateLimiter keeps the counts in the store, under the reserved rate-limit
// declaration's keys, one per service, operation, window and client, where
// the client is the SHA-256 of the limiter's key in hex, so no address is
// in the store.
//
// RateLimiter holds no lock: a request reads both windows' counters in one
// round trip and, when allowed, increments the current one in a second,
// so the requests of a route overlap. Requests in flight together may each
// pass on the counts before the others' increments, as requests on two
// replicas may. While the store is unavailable, or a call to it fails,
// Take returns the error, and the route limits the request in process
// instead, each replica on its own budget, as it did before it had a
// store.

// RateLimitDeclaration is the reserved declaration whose keys the shared
// rate limit counts in.
const RateLimitDeclaration = ir.MemoryStoreRateLimit

// RateLimitMaxTTL is the longest TTL a rate-limit key may have: its
// declaration's maxTtlSeconds.
const RateLimitMaxTTL = ir.MemoryStoreRateLimitMaxTTLSeconds * time.Second

// rateLimitCallTimeout bounds a request's calls to the store.
const rateLimitCallTimeout = time.Second

// RateLimitRef is the ref of the counter of service's operation in the
// window starting at windowStart, in Unix seconds, for the limiter's
// client key: `rate-limit:<service>:<operation>:<window>:<sha256(client)>`.
func RateLimitRef(service, operation string, windowStart int64, clientKey string) (Ref, error) {
	sum := sha256.Sum256([]byte(clientKey))
	name, err := Name(RateLimitDeclaration, service, operation, strconv.FormatInt(windowStart, 10), hex.EncodeToString(sum[:]))
	if err != nil {
		return Ref{}, err
	}
	return Ref{Declaration: RateLimitDeclaration, Name: name}, nil
}

// RateLimitTTL is the TTL of a window's counter: two windows and five
// seconds, so the previous window's count outlives the window that reads
// it.
func RateLimitTTL(window time.Duration) time.Duration {
	return 2*window + ir.MemoryStoreRateLimitWindowSlackSeconds*time.Second
}

// RateLimiter is the shared rate limit of one route: service's operation,
// at most limit requests per client in a sliding window of window, counted
// in store.
type RateLimiter struct {
	store     Store
	service   string
	operation string
	limit     int
	window    time.Duration
}

// NewRateLimiter limits service's operation to limit requests per client
// in each window, in store.
func NewRateLimiter(store Store, service, operation string, limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{store: store, service: service, operation: operation, limit: limit, window: window}
}

// RateLimitDecision is the shared rate limit's answer to one request, with
// what httprate's headers carry.
type RateLimitDecision struct {
	// Allowed reports whether the request may go on; it was counted.
	Allowed bool
	// Limit is the route's limit, and Remaining what is left of it in the
	// window: the limit less the rounded estimate, and less this request
	// when it is allowed.
	Limit     int
	Remaining int
	// Reset is the end of the current window.
	Reset time.Time
	// RetryAfterSeconds is the window's length in whole seconds when the
	// request is refused, 0 when it is allowed.
	RetryAfterSeconds int64
}

// Take decides the request of the client whose limiter key is clientKey,
// at now, and counts it when it is allowed. It returns an error, which
// wraps ErrUnavailable while the store is unavailable, when the store
// cannot decide: the caller limits the request in process instead.
func (l *RateLimiter) Take(ctx context.Context, clientKey string, now time.Time) (RateLimitDecision, error) {
	if l.store == nil || !l.store.Available() {
		return RateLimitDecision{}, ErrUnavailable
	}
	ttl := RateLimitTTL(l.window)
	windowMs := l.window.Milliseconds()
	if windowMs <= 0 || ttl > RateLimitMaxTTL {
		return RateLimitDecision{}, ErrUnavailable
	}
	curr := time.UnixMilli(now.UnixMilli() / windowMs * windowMs)
	prev := curr.Add(-l.window)
	currRef, err := RateLimitRef(l.service, l.operation, curr.Unix(), clientKey)
	if err != nil {
		return RateLimitDecision{}, err
	}
	prevRef, err := RateLimitRef(l.service, l.operation, prev.Unix(), clientKey)
	if err != nil {
		return RateLimitDecision{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, rateLimitCallTimeout)
	defer cancel()
	counts, err := Counts(ctx, l.store, prevRef, currRef)
	if err != nil {
		return RateLimitDecision{}, err
	}
	estimate := slidingWindowEstimate(windowMs, now.Sub(curr), counts[0], counts[1])
	rate := int(math.Round(estimate))
	d := RateLimitDecision{Limit: l.limit, Reset: curr.Add(l.window)}
	if rate+1 > l.limit {
		d.Remaining = l.limit - rate
		d.RetryAfterSeconds = int64(l.window.Seconds())
		return d, nil
	}
	if _, err := l.store.Increment(ctx, currRef, ttl); err != nil {
		return RateLimitDecision{}, err
	}
	d.Allowed = true
	d.Remaining = l.limit - rate - 1
	return d, nil
}

// slidingWindowEstimate is prevCount * (windowMs - elapsed) / windowMs +
// currCount, in milliseconds and IEEE doubles, in that order, as the
// vectors write it.
func slidingWindowEstimate(windowMs int64, elapsed time.Duration, prevCount, currCount int64) float64 {
	w := float64(windowMs)
	e := float64(elapsed) / float64(time.Millisecond)
	return float64(prevCount)*(w-e)/w + float64(currCount)
}

// MultiCounter is a store that reads several counters in one round trip.
type MultiCounter interface {
	// Counts returns the counters refs name, in order: 0 for a key that
	// does not exist.
	Counts(ctx context.Context, refs ...Ref) ([]int64, error)
}

// Counts returns the counters refs name, in order: in one call when store
// is a MultiCounter, and otherwise by a Count for each, all at once.
func Counts(ctx context.Context, store Store, refs ...Ref) ([]int64, error) {
	if m, ok := store.(MultiCounter); ok {
		return m.Counts(ctx, refs...)
	}
	counts := make([]int64, len(refs))
	errs := make([]error, len(refs))
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Go(func() { counts[i], errs[i] = store.Count(ctx, ref) })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return counts, nil
}
