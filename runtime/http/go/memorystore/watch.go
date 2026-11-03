package memorystore

import (
	"context"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"
)

// Timings bound a networked store's calls and its health ping. The zero
// Timings is DefaultTimings; a test shortens them.
type Timings struct {
	// Dial bounds one connection attempt, and Ping one health ping.
	Dial time.Duration
	Ping time.Duration

	// Op bounds one command's read or write.
	Op time.Duration

	// Interval is the time between health pings while the store is up.
	Interval time.Duration

	// MinBackoff is the first wait after a failed ping, which doubles to
	// MaxBackoff while the store stays down, each spread by up to 20%
	// either way so replicas do not retry in step.
	MinBackoff time.Duration
	MaxBackoff time.Duration

	// ReceiveIdle is how long a subscriber connection waits for a message
	// before it pings the server.
	ReceiveIdle time.Duration
}

// DefaultTimings are a store's timings unless a test sets others: a dial
// and a ping within a second, 500ms for an operation, a health ping every
// two seconds, and a backoff from 100ms to five seconds.
var DefaultTimings = Timings{
	Dial:        time.Second,
	Ping:        time.Second,
	Op:          500 * time.Millisecond,
	Interval:    2 * time.Second,
	MinBackoff:  100 * time.Millisecond,
	MaxBackoff:  5 * time.Second,
	ReceiveIdle: 15 * time.Second,
}

// OrDefault is t with each zero duration DefaultTimings'.
func (t Timings) OrDefault() Timings {
	d := DefaultTimings
	for _, f := range []struct{ got, def *time.Duration }{
		{&t.Dial, &d.Dial}, {&t.Ping, &d.Ping}, {&t.Op, &d.Op}, {&t.Interval, &d.Interval},
		{&t.MinBackoff, &d.MinBackoff}, {&t.MaxBackoff, &d.MaxBackoff}, {&t.ReceiveIdle, &d.ReceiveIdle},
	} {
		if *f.got <= 0 {
			*f.got = *f.def
		}
	}
	return t
}

// Jitter spreads d by up to 20% either way.
func Jitter(d time.Duration) time.Duration {
	spread := int64(d) / 5
	if spread <= 0 {
		return d
	}
	return d - time.Duration(spread) + time.Duration(rand.Int64N(2*spread+1))
}

// NextBackoff doubles d, up to max.
func NextBackoff(d, max time.Duration) time.Duration {
	return min(2*d, max)
}

// Watch follows a networked store's connection: it pings the server once
// when it starts, then on an interval while the store is up and with a
// jittered backoff while it is down, so Available follows the server. A
// call that fails on the connection marks the store down at once, which
// wakes the loop to ping again. It never blocks a caller on the server.
type Watch struct {
	ping     func(context.Context) error
	onChange func(up bool, cause error)
	tm       Timings

	available atomic.Bool
	wake      chan struct{}
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	stopOnce  sync.Once
}

// NewWatch is a Watch that pings with ping and reports each change of the
// connection's state to onChange, nil for none.
func NewWatch(ping func(context.Context) error, onChange func(up bool, cause error), tm Timings) *Watch {
	if onChange == nil {
		onChange = func(bool, error) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Watch{ping: ping, onChange: onChange, tm: tm.OrDefault(), wake: make(chan struct{}, 1), ctx: ctx, cancel: cancel}
}

// Start pings the server once, within the ping's bound and ctx, and starts
// the loop in the background. It reports whether the server answered; a
// store that did not is down and reconnects in the background.
func (w *Watch) Start(ctx context.Context) bool {
	pingCtx, cancel := context.WithTimeout(ctx, w.tm.Ping)
	err := w.ping(pingCtx)
	cancel()
	w.set(err == nil, err)
	w.wg.Add(1)
	go w.loop(err == nil)
	return err == nil
}

// Available reports whether the last ping, or call, reached the server.
func (w *Watch) Available() bool {
	return w.available.Load()
}

// MarkDown records a call that failed on the connection, and wakes the
// loop to ping at once.
func (w *Watch) MarkDown(cause error) {
	if !w.available.Load() {
		return
	}
	w.set(false, cause)
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Done is closed once Stop is called.
func (w *Watch) Done() <-chan struct{} {
	return w.ctx.Done()
}

// Stop ends the loop and waits for it; Available is false after it. It is
// safe to call more than once.
func (w *Watch) Stop() {
	w.stopOnce.Do(func() {
		w.cancel()
		w.wg.Wait()
		w.available.Store(false)
	})
}

func (w *Watch) set(up bool, cause error) {
	if was := w.available.Swap(up); was != up {
		w.onChange(up, cause)
	}
}

func (w *Watch) loop(up bool) {
	defer w.wg.Done()
	backoff := w.tm.MinBackoff
	wait := w.tm.Interval
	if !up {
		wait = Jitter(backoff)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-w.wake:
			timer.Stop()
		case <-timer.C:
		}
		ctx, cancel := context.WithTimeout(w.ctx, w.tm.Ping)
		err := w.ping(ctx)
		cancel()
		if w.ctx.Err() != nil {
			return
		}
		w.set(err == nil, err)
		if err == nil {
			backoff, wait = w.tm.MinBackoff, w.tm.Interval
		} else {
			wait = Jitter(backoff)
			backoff = NextBackoff(backoff, w.tm.MaxBackoff)
		}
		timer.Reset(wait)
	}
}

// Sleep waits for d or until ctx ends, and reports whether ctx is still
// live.
func Sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
