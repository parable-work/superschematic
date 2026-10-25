package memorystore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/httprate"
	"github.com/parable-work/superschematic/ir"
)

// The shared rate limit (D75). An API whose config names a rateLimitStore
// counts each @rateLimit route in that store, so every replica spends one
// budget. The limiter is still httprate's, with its sliding window: the
// previous window's count weighted by the part of it still in the window,
// plus this window's, and a request is allowed while that estimate,
// rounded, plus one is at most the limit. RateLimitCounter is the
// httprate.LimitCounter that keeps the counts in the store, under the
// reserved rate-limit declaration's keys, one per service, operation,
// window and client, where the client is the SHA-256 of the limiter's key
// in hex, so no address is in the store.
//
// httprate answers 428 when its counter returns an error, so the counter
// never returns one: while the store is unavailable, or a call to it
// fails, it counts in process instead, each replica on its own budget, as
// the route did before it had a store.

// RateLimitDeclaration is the reserved declaration whose keys the shared
// rate limit counts in.
const RateLimitDeclaration = ir.MemoryStoreRateLimit

// RateLimitMaxTTL is the longest TTL a rate-limit key may have: its
// declaration's maxTtlSeconds.
const RateLimitMaxTTL = ir.MemoryStoreRateLimitMaxTTLSeconds * time.Second

// rateLimitCallTimeout bounds one call of the counter to the store, which
// httprate makes without a context.
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

// RateLimitCounter is an httprate.LimitCounter that counts service's
// operation in store, and in process while the store cannot.
type RateLimitCounter struct {
	store     Store
	service   string
	operation string

	mu     sync.Mutex
	window time.Duration
	local  httprate.LimitCounter
}

var _ httprate.LimitCounter = (*RateLimitCounter)(nil)

// NewRateLimitCounter counts service's operation in store.
func NewRateLimitCounter(store Store, service, operation string) *RateLimitCounter {
	return &RateLimitCounter{store: store, service: service, operation: operation}
}

// Config is httprate's: the limiter's window, which sets each key's TTL.
func (c *RateLimitCounter) Config(_ int, window time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.window = window
	c.local = httprate.NewLocalLimitCounter(window)
}

// Increment counts one request of key in the window starting at
// currentWindow.
func (c *RateLimitCounter) Increment(key string, currentWindow time.Time) error {
	return c.IncrementBy(key, currentWindow, 1)
}

// IncrementBy counts amount requests of key in the window starting at
// currentWindow: in the store, or in process while it cannot.
func (c *RateLimitCounter) IncrementBy(key string, currentWindow time.Time, amount int) error {
	window, local := c.config()
	if ref, ttl, ok := c.ref(key, currentWindow, window); ok {
		ctx, cancel := context.WithTimeout(context.Background(), rateLimitCallTimeout)
		defer cancel()
		counted := 0
		for ; counted < amount; counted++ {
			if _, err := c.store.Increment(ctx, ref, ttl); err != nil {
				break
			}
		}
		if counted == amount {
			return nil
		}
		amount -= counted
	}
	return local.IncrementBy(key, currentWindow, amount)
}

// Get returns key's counts in the current and the previous window: the
// store's, or the process's while the store cannot answer both.
func (c *RateLimitCounter) Get(key string, currentWindow, previousWindow time.Time) (int, int, error) {
	window, local := c.config()
	curr, ttl, ok := c.ref(key, currentWindow, window)
	prev, _, prevOK := c.ref(key, previousWindow, window)
	if ok && prevOK && ttl > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), rateLimitCallTimeout)
		defer cancel()
		currCount, err := c.store.Count(ctx, curr)
		if err == nil {
			var prevCount int64
			if prevCount, err = c.store.Count(ctx, prev); err == nil {
				return int(currCount), int(prevCount), nil
			}
		}
	}
	return local.Get(key, currentWindow, previousWindow)
}

func (c *RateLimitCounter) config() (time.Duration, httprate.LimitCounter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.local == nil {
		// httprate calls Config before any other method; a counter used
		// without it counts per minute.
		c.window = time.Minute
		c.local = httprate.NewLocalLimitCounter(c.window)
	}
	return c.window, c.local
}

// ref is the ref and TTL of key's counter in the window starting at
// start, and false when the store cannot take it: it is unavailable, or
// the service, the operation or the window cannot name a key.
func (c *RateLimitCounter) ref(key string, start time.Time, window time.Duration) (Ref, time.Duration, bool) {
	if c.store == nil || !c.store.Available() {
		return Ref{}, 0, false
	}
	ttl := RateLimitTTL(window)
	if window <= 0 || ttl > RateLimitMaxTTL {
		return Ref{}, 0, false
	}
	ref, err := RateLimitRef(c.service, c.operation, start.Unix(), key)
	if err != nil {
		return Ref{}, 0, false
	}
	return ref, ttl, true
}
