package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrNotOpen is what a Background engine answers until it opens: its
// database has not answered yet, or its setup has not finished. The error
// a Background returns wraps it, with the last attempt's failure.
var ErrNotOpen = errors.New("engine: not open yet")

// The waits between a Background's attempts unless its options set them:
// a second, doubling to thirty seconds.
const (
	DefaultRetryMin = time.Second
	DefaultRetryMax = 30 * time.Second
)

// BackgroundOptions are how a Background opens its engine.
type BackgroundOptions struct {
	// Setup runs on the engine once it opens, before the Background hands
	// it out: a server's Deploy, a runner's Runner().Start. An error closes
	// the engine, and the next attempt opens it again. Nil sets nothing up.
	Setup func(ctx context.Context, e *Engine) error

	// RetryMin and RetryMax bound the wait between attempts, which doubles
	// from RetryMin to RetryMax: DefaultRetryMin and DefaultRetryMax unless
	// set.
	RetryMin, RetryMax time.Duration

	// OnError is told of each failed attempt and how long the Background
	// waits before the next; OnOpen of the open engine, once its setup
	// passed. Each runs on the Background's goroutine.
	OnError func(err error, retry time.Duration)
	OnOpen  func(e *Engine)
}

// Background is an engine a server opens while it serves (D58): the
// server starts while its database is not up yet, as its connection pools
// do, and reports the engine unavailable, in its readiness and on the
// engine's routes, until the engine opens and its setup passes. Its
// methods are safe to call from many goroutines.
type Background struct {
	options BackgroundOptions
	ctx     context.Context
	cancel  context.CancelFunc
	opened  chan struct{}

	mu     sync.Mutex
	engine *Engine
	err    error
	closed bool
}

// OpenInBackground opens an engine on cfg on a goroutine of its own,
// trying again after each failure, the open's or the setup's, until it
// opens or the Background closes.
func OpenInBackground(cfg Config, options BackgroundOptions) *Background {
	if options.RetryMin <= 0 {
		options.RetryMin = DefaultRetryMin
	}
	if options.RetryMax < options.RetryMin {
		options.RetryMax = max(DefaultRetryMax, options.RetryMin)
	}
	ctx, cancel := context.WithCancel(context.Background())
	b := &Background{options: options, ctx: ctx, cancel: cancel, opened: make(chan struct{})}
	go b.run(cfg)
	return b
}

// run opens the engine, sets it up, and hands it out, trying again after
// each failure.
func (b *Background) run(cfg Config) {
	wait := b.options.RetryMin
	for {
		e, err := b.attempt(cfg)
		if err == nil {
			b.mu.Lock()
			if b.closed {
				b.mu.Unlock()
				_ = e.Close()
				return
			}
			b.engine, b.err = e, nil
			b.mu.Unlock()
			close(b.opened)
			if b.options.OnOpen != nil {
				b.options.OnOpen(e)
			}
			return
		}
		b.mu.Lock()
		b.err = err
		closed := b.closed
		b.mu.Unlock()
		if closed {
			return
		}
		if b.options.OnError != nil {
			b.options.OnError(err, wait)
		}
		timer := time.NewTimer(wait)
		select {
		case <-b.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		wait = min(wait*2, b.options.RetryMax)
	}
}

// attempt opens the engine and runs the setup on it, closing it when the
// setup fails.
func (b *Background) attempt(cfg Config) (*Engine, error) {
	e, err := Open(cfg)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	if b.options.Setup != nil {
		if err := b.options.Setup(b.ctx, e); err != nil {
			_ = e.Close()
			return nil, err
		}
	}
	return e, nil
}

// Engine is the open engine, or an error wrapping ErrNotOpen, with why the
// last attempt failed, until it opens; ErrClosed once the Background
// closed.
func (b *Background) Engine() (*Engine, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case b.closed:
		return nil, ErrClosed
	case b.engine != nil:
		return b.engine, nil
	case b.err != nil:
		return nil, fmt.Errorf("%w: %w", ErrNotOpen, b.err)
	}
	return nil, ErrNotOpen
}

// Wait waits for the engine to open, or for ctx, and returns it.
func (b *Background) Wait(ctx context.Context) (*Engine, error) {
	select {
	case <-b.opened:
	case <-b.ctx.Done():
	case <-ctx.Done():
		if _, err := b.Engine(); err != nil {
			return nil, fmt.Errorf("%w: %w", ctx.Err(), err)
		}
	}
	return b.Engine()
}

// Opened returns a channel closed once the engine has opened.
func (b *Background) Opened() <-chan struct{} {
	return b.opened
}

// Ready is nil while the engine is open and ready (Engine.Ready), else
// why not: what a server's readiness checks.
func (b *Background) Ready(ctx context.Context) error {
	e, err := b.Engine()
	if err != nil {
		return err
	}
	return e.Ready(ctx)
}

// Close stops trying to open the engine and closes it if it is open. It
// does not wait for an attempt in flight, whose open the archive bounds by
// its connect timeout: what that attempt opens closes when it returns.
// Closing a closed Background does nothing.
func (b *Background) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	e := b.engine
	b.mu.Unlock()
	b.cancel()
	if e != nil {
		return e.Close()
	}
	return nil
}
