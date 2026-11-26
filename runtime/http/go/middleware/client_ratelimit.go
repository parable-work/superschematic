package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync/atomic"
	"time"

	"github.com/go-chi/httprate"
	"go.uber.org/zap"
)

// ErrClientRateLimitStoreUnavailable is the error a ClientRateLimitStore
// wraps while it is disabled or down. The store logs that state itself, so
// a limiter falls back to its replica's count without a log line of its
// own.
var ErrClientRateLimitStoreUnavailable = errors.New("client rate limit store unavailable")

// ClientRateLimitWindow names the count of one client in one window of one
// limiter.
type ClientRateLimitWindow struct {
	// Limiter names the limiter. It is the same on every replica and unique
	// to one limiter: the generated router names a route's limiter by its
	// method and path, such as "POST /api/sessions".
	Limiter string
	// Client is a keyed hash of what the limiter counts per, a client
	// address or the name authentication gave the client. It never holds
	// the value itself.
	Client string
	// Start is the start of the window, aligned to the wall clock.
	Start time.Time
	// Length is the length of the window.
	Length time.Duration
}

// ClientRateLimitStore holds the counts behind per-client rate limits, so
// that every replica of an API that shares the store counts one limit.
//
// Each call is short. httprate holds one lock per limiter across Counts and
// Add, so every request of the limiter on the replica waits for the call in
// flight. A store that is down returns at once, with an error that wraps
// ErrClientRateLimitStoreUnavailable. A call that has not answered in
// clientRateLimitStoreTimeout counts as failed: the store must stop when
// its context ends.
type ClientRateLimitStore interface {
	// Add adds n to the count of window. The count must live until the end
	// of the next window, because the next window reads it as its previous
	// window.
	Add(ctx context.Context, window ClientRateLimitWindow, n int) error
	// Counts returns the counts of current and previous. A window with no
	// count counts zero.
	Counts(ctx context.Context, current, previous ClientRateLimitWindow) (int, int, error)
}

// clientRateLimitHashLabel separates the hash key from every other use of
// the secret that NewClientRateLimits takes.
const clientRateLimitHashLabel = "superschematic http runtime client rate limit v1"

// clientRateLimitHashBytes is the length of a Client hash: 128 bits of the
// HMAC, enough that two clients of one limiter never share a count.
const clientRateLimitHashBytes = 16

// clientRateLimitMinSecretBytes refuses a secret too short to keep the hash
// of an IPv4 address from a brute-force search.
const clientRateLimitMinSecretBytes = 16

// clientRateLimitStoreTimeout bounds one store call. httprate holds the
// limiter's lock during the call, so a slow store makes every request of
// the limiter on the replica wait in turn. A call that takes longer fails,
// and the limiter counts in the replica for clientRateLimitCooldown.
const clientRateLimitStoreTimeout = 50 * time.Millisecond

// clientRateLimitCooldown is how long a limiter counts in the replica alone
// after a store call fails, before it calls the store again. Without it, a
// store that answers just under the timeout would hold each request for
// two calls.
const clientRateLimitCooldown = 5 * time.Second

// clientRateLimitWarnInterval spaces the log lines of store errors that do
// not wrap ErrClientRateLimitStoreUnavailable.
const clientRateLimitWarnInterval = time.Minute

// ClientRateLimits shares the per-client rate limits of an API between its
// replicas. Without it, httprate counts each limit in the memory of each
// replica, so an API behind N replicas admits N times the declared rate.
// The generated router takes it as Config.ClientRateLimits and passes it to
// each per-client @rateLimit route with WithClientRateLimits.
//
// Each limiter gets its own httprate counter from Counter. The counter
// keeps httprate's sliding window: a request is refused when the count of
// the current window, plus the count of the previous window weighted by the
// part of it that is still in the last window length, reaches the limit.
// Windows are aligned to the wall clock, so every replica reads the same
// window keys.
//
// The store sees a keyed hash of each client key, never the key itself: a
// plain hash of an IPv4 address gives the address back in seconds. The hash
// key comes from a secret that every replica holds and the store never
// sees.
//
// The counter fails open, as the in-memory counter does. It always counts
// in its replica's memory too. When a store call fails or takes longer than
// clientRateLimitStoreTimeout, the limiter reads and counts in the
// replica's memory alone for clientRateLimitCooldown. When the store
// answers, each window counts the larger of the store's count and the
// replica's own, so a count that the store lost never admits more than the
// replica would alone.
//
// httprate reads the count and adds to it in two steps, under one lock per
// limiter. So one replica admits one request of a limiter at a time, and
// replicas that read the same count at the same moment can each admit one:
// the limit admits at most one request more per replica.
type ClientRateLimits struct {
	store   ClientRateLimitStore
	hashKey []byte
	logger  *zap.Logger
	now     func() time.Time

	// lastWarning is the Unix time in nanoseconds of the last store error
	// logged, or zero.
	lastWarning atomic.Int64
}

// NewClientRateLimits returns the shared limits of one API. store holds the
// counts. secret is a secret that every replica holds and store never sees,
// such as the API's session signing secret: the hash key is an HMAC of a
// label of its own under it, so a count reveals neither the secret nor the
// client. NewClientRateLimits refuses a nil store and a secret shorter than
// 16 bytes. A nil logger logs nothing.
func NewClientRateLimits(store ClientRateLimitStore, secret []byte, logger *zap.Logger) (*ClientRateLimits, error) {
	if store == nil {
		return nil, errors.New("client rate limits need a store")
	}
	if len(secret) < clientRateLimitMinSecretBytes {
		return nil, errors.New("client rate limits need a secret of at least 16 bytes")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(clientRateLimitHashLabel))
	return &ClientRateLimits{store: store, hashKey: mac.Sum(nil), logger: logger, now: time.Now}, nil
}

// Counter returns the httprate counter of the limiter named limiter, for
// httprate.WithLimitCounter. Give each limiter its own name: two limiters
// with one name share their counts. On a nil ClientRateLimits it returns
// nil, and httprate then counts in the replica's memory.
func (l *ClientRateLimits) Counter(limiter string) httprate.LimitCounter {
	if l == nil {
		return nil
	}
	return &clientRateLimitCounter{
		limits:  l,
		limiter: limiter,
		local:   httprate.NewLocalLimitCounter(time.Minute),
		length:  time.Minute,
	}
}

// WithClientRateLimits counts the limit in limits under the name limiter,
// so that every replica that passes the same limits counts one limit. The
// generated router names each route's limiter by its method and path. A
// nil limits changes nothing: the replica counts alone.
func WithClientRateLimits(limits *ClientRateLimits, limiter string) RateLimitOption {
	return func(c *rateLimitConfig) {
		if counter := limits.Counter(limiter); counter != nil {
			c.counter = counter
		}
	}
}

// hash returns the Client of key: a keyed hash, in hex.
func (l *ClientRateLimits) hash(key string) string {
	mac := hmac.New(sha256.New, l.hashKey)
	mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil)[:clientRateLimitHashBytes])
}

// failed logs a store error, at most once per clientRateLimitWarnInterval.
// An error that wraps ErrClientRateLimitStoreUnavailable is not logged: the
// store logs its own state.
func (l *ClientRateLimits) failed(limiter string, err error) {
	if errors.Is(err, ErrClientRateLimitStoreUnavailable) {
		return
	}
	now := l.now().UnixNano()
	last := l.lastWarning.Load()
	if last != 0 && now-last < int64(clientRateLimitWarnInterval) {
		return
	}
	if !l.lastWarning.CompareAndSwap(last, now) {
		return
	}
	l.logger.Warn("shared rate limit store failed or was slow: the limiter counts in this replica alone for a few seconds",
		zap.String("limiter", limiter), zap.Error(err))
}

// clientRateLimitCounter is the httprate.LimitCounter of one limiter.
// httprate calls it under the limiter's lock.
type clientRateLimitCounter struct {
	limits  *ClientRateLimits
	limiter string
	// local counts in the replica's memory, always.
	local  httprate.LimitCounter
	length time.Duration
	// alone is the Unix time in nanoseconds until which the limiter counts
	// in the replica alone, after a failed store call.
	alone atomic.Int64
}

var _ httprate.LimitCounter = (*clientRateLimitCounter)(nil)

// Config implements httprate.LimitCounter. httprate calls it once, when it
// builds the limiter.
func (c *clientRateLimitCounter) Config(requestLimit int, windowLength time.Duration) {
	c.length = windowLength
	c.local.Config(requestLimit, windowLength)
}

// Increment implements httprate.LimitCounter.
func (c *clientRateLimitCounter) Increment(key string, currentWindow time.Time) error {
	return c.IncrementBy(key, currentWindow, 1)
}

// IncrementBy implements httprate.LimitCounter. It never returns an error:
// httprate refuses a request when its counter fails, and the limit fails
// open.
func (c *clientRateLimitCounter) IncrementBy(key string, currentWindow time.Time, amount int) error {
	_ = c.local.IncrementBy(key, currentWindow, amount)
	if !c.shared() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), clientRateLimitStoreTimeout)
	defer cancel()
	if err := c.limits.store.Add(ctx, c.window(c.limits.hash(key), currentWindow), amount); err != nil {
		c.failed(err)
	}
	return nil
}

// Get implements httprate.LimitCounter. It never returns an error, as
// IncrementBy does not.
func (c *clientRateLimitCounter) Get(key string, currentWindow, previousWindow time.Time) (int, int, error) {
	localCurrent, localPrevious, _ := c.local.Get(key, currentWindow, previousWindow)
	if !c.shared() {
		return localCurrent, localPrevious, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), clientRateLimitStoreTimeout)
	defer cancel()
	client := c.limits.hash(key)
	current, previous, err := c.limits.store.Counts(ctx, c.window(client, currentWindow), c.window(client, previousWindow))
	if err != nil {
		c.failed(err)
		return localCurrent, localPrevious, nil
	}
	return max(current, localCurrent), max(previous, localPrevious), nil
}

// shared reports whether the limiter calls the store now, or counts in the
// replica alone after a failed call.
func (c *clientRateLimitCounter) shared() bool {
	return c.limits.now().UnixNano() >= c.alone.Load()
}

// failed makes the limiter count in the replica alone for
// clientRateLimitCooldown, and logs the error.
func (c *clientRateLimitCounter) failed(err error) {
	c.alone.Store(c.limits.now().Add(clientRateLimitCooldown).UnixNano())
	c.limits.failed(c.limiter, err)
}

func (c *clientRateLimitCounter) window(client string, start time.Time) ClientRateLimitWindow {
	return ClientRateLimitWindow{Limiter: c.limiter, Client: client, Start: start, Length: c.length}
}
