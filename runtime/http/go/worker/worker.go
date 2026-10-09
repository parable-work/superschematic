// Package worker is the claim loop of a stack's worker (docs/stack-model.md,
// section 8.8, D53): the process that claims the messages of a queue of a
// database and handles each with an API's Deps. The worker's generated
// entrypoint builds a Queue from the claims the database's ORM makes, each
// message with the handler of the API's Workers interface, and calls Run.
//
// Run claims up to its concurrency of messages at a time, and handles each
// in a goroutine of its own, extending the message's claim while the
// handler runs, so a handler longer than the lease keeps its message. A
// handler that returns nil completes its message; one that returns an
// error, or panics, fails it, which the queue retries after its backoff or
// marks dead. Each outcome is logged. When its context ends (SIGTERM), Run
// claims nothing more, gives the running handlers the grace to finish,
// then cancels them and releases their messages, which are due again at
// once for another worker, and returns.
//
// The package knows no database: the ORM's claims are the functions of
// Queue, so the loop is the same for every queue and every dialect.
package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ConcurrencyVariable is the environment variable a platform sets to the
// number of messages a worker handles at a time in its environment: the
// worker's settings or its `@worker` concurrency.
const ConcurrencyVariable = "WORKER_CONCURRENCY"

// The defaults of Options.
const (
	// DefaultGrace is how long a stopping worker lets its handlers finish:
	// under the ten seconds Cloud Run gives a container after SIGTERM.
	DefaultGrace = 8 * time.Second

	// DefaultIdle is how long a worker waits to claim again after a claim
	// found no message due.
	DefaultIdle = time.Second

	// maxBackoff caps the wait after a claim that failed, which doubles
	// from the idle wait.
	maxBackoff = 30 * time.Second

	// opTimeout bounds each complete, fail, extend and release, which run
	// on a context of their own, so a stopping worker still records them.
	opTimeout = 5 * time.Second

	// maxCause caps the error a failed message records.
	maxCause = 4000
)

// Message is one message a worker claimed: its ID, the claim's token, the
// attempt this is, from 1, and Handle, which runs the worker's handler on
// the message.
type Message struct {
	ID      string
	Token   string
	Attempt int
	Handle  func(ctx context.Context) error
}

// Queue is the queue a worker claims from, as the ORM of its database
// claims (D53). Every function is required.
type Queue struct {
	// Name names the queue in the log.
	Name string

	// Lease is how long a claim holds a message; Run extends it every third
	// of it while the message's handler runs.
	Lease time.Duration

	// Claim claims up to n due messages for lease.
	Claim func(ctx context.Context, n int, lease time.Duration) ([]Message, error)

	// Extend holds a claimed message for lease from now.
	Extend func(ctx context.Context, id, token string, lease time.Duration) error

	// Complete marks a claimed message done.
	Complete func(ctx context.Context, id, token string) error

	// Fail records cause on a claimed message, which the queue retries
	// after its backoff or marks dead, and reports whether it is dead.
	Fail func(ctx context.Context, id, token, cause string) (dead bool, err error)

	// Release gives a claimed message back, due at once.
	Release func(ctx context.Context, id, token string) error
}

// Options are how Run runs.
type Options struct {
	// Concurrency is how many messages run at a time; less than one is
	// one.
	Concurrency int

	// Grace is how long a stopping worker lets its running handlers
	// finish before it cancels them and releases their messages; zero is
	// DefaultGrace.
	Grace time.Duration

	// Idle is how long Run waits to claim again after a claim found
	// nothing; zero is DefaultIdle.
	Idle time.Duration

	// Logger logs each message's outcome; nil logs nothing.
	Logger *zap.Logger
}

// Concurrency returns the concurrency the environment gives a worker in
// ConcurrencyVariable, or def when it sets none.
func Concurrency(def int) (int, error) {
	value := os.Getenv(ConcurrencyVariable)
	if value == "" {
		return def, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s is %q; it is a whole number, one or more", ConcurrencyVariable, value)
	}
	return n, nil
}

// Run claims and handles q's messages until ctx ends, then stops as the
// package says: it claims no more, lets the running handlers finish within
// the grace, and releases the messages of those that do not. It returns
// nil once it stopped, and an error only for a Queue or Options it cannot
// run.
func Run(ctx context.Context, q Queue, opts Options) error {
	if q.Claim == nil || q.Extend == nil || q.Complete == nil || q.Fail == nil || q.Release == nil {
		return errors.New("worker: the queue lacks a claim function")
	}
	if q.Lease <= 0 {
		return fmt.Errorf("worker: queue %s has a lease of %s; it is positive", q.Name, q.Lease)
	}
	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}
	if opts.Grace <= 0 {
		opts.Grace = DefaultGrace
	}
	if opts.Idle <= 0 {
		opts.Idle = DefaultIdle
	}
	logger := opts.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	logger = logger.With(zap.String("queue", q.Name))
	w := &loop{q: q, opts: opts, logger: logger, running: map[string]*handling{}, done: make(chan struct{}, opts.Concurrency)}
	// The handlers' context outlives ctx by the grace: a stopping worker
	// cancels it only once the grace is over.
	w.handlers, w.cancelHandlers = context.WithCancel(context.WithoutCancel(ctx))
	defer w.cancelHandlers()
	w.claimLoop(ctx)
	return w.stop()
}

// loop is one Run.
type loop struct {
	q      Queue
	opts   Options
	logger *zap.Logger

	handlers       context.Context
	cancelHandlers context.CancelFunc
	wg             sync.WaitGroup

	mu sync.Mutex
	// running are the messages whose handlers run, by ID.
	running map[string]*handling
	// done receives once per handler that returns, so the claim loop
	// wakes to claim for the slot it frees.
	done chan struct{}
}

// handling is one message whose handler runs.
type handling struct {
	msg      Message
	started  time.Time
	released bool
}

// claimLoop claims messages for the free slots until ctx ends.
func (w *loop) claimLoop(ctx context.Context) {
	wait := w.opts.Idle
	for ctx.Err() == nil {
		free := w.opts.Concurrency - w.count()
		if free <= 0 {
			select {
			case <-ctx.Done():
				return
			case <-w.done:
			}
			continue
		}
		msgs, err := w.q.Claim(ctx, free, w.q.Lease)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			w.logger.Error("claim failed", zap.Duration("retryIn", wait), zap.Error(err))
			if !sleep(ctx, wait) {
				return
			}
			wait = min(wait*2, maxBackoff)
			continue
		}
		wait = w.opts.Idle
		for _, msg := range msgs {
			w.start(msg)
		}
		if len(msgs) < free && !w.idle(ctx) {
			return
		}
	}
}

// idle waits the idle wait after a claim that filled no slot, or until ctx
// ends, and reports whether ctx is still live.
func (w *loop) idle(ctx context.Context) bool {
	return sleep(ctx, w.opts.Idle)
}

// count is how many handlers run.
func (w *loop) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.running)
}

// start runs a message's handler in a goroutine, extending its claim while
// it runs.
func (w *loop) start(msg Message) {
	h := &handling{msg: msg, started: time.Now()}
	w.mu.Lock()
	w.running[msg.ID] = h
	w.mu.Unlock()
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer func() {
			select {
			case w.done <- struct{}{}:
			default:
			}
		}()
		extended := make(chan struct{})
		stopExtending := w.extend(msg, extended)
		err := handle(w.handlers, msg)
		stopExtending()
		<-extended
		w.finish(h, err)
	}()
}

// extend extends msg's claim every third of the lease until the returned
// function is called, then closes extended.
func (w *loop) extend(msg Message, extended chan struct{}) func() {
	stop := make(chan struct{})
	go func() {
		defer close(extended)
		ticker := time.NewTicker(w.q.Lease / 3)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
				err := w.q.Extend(ctx, msg.ID, msg.Token, w.q.Lease)
				cancel()
				if err != nil {
					w.logger.Warn("extend the claim failed", zap.String("message", msg.ID), zap.Int("attempt", msg.Attempt), zap.Error(err))
				}
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(stop) }) }
}

// handle runs msg's handler, turning a panic into an error.
func handle(ctx context.Context, msg Message) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v\n%s", p, debug.Stack())
		}
	}()
	return msg.Handle(ctx)
}

// finish records a handler's outcome, unless its message was released
// while it ran.
func (w *loop) finish(h *handling, err error) {
	w.mu.Lock()
	delete(w.running, h.msg.ID)
	released := h.released
	w.mu.Unlock()
	msg, took := h.msg, time.Since(h.started)
	fields := []zap.Field{zap.String("message", msg.ID), zap.Int("attempt", msg.Attempt), zap.Duration("took", took)}
	if released {
		w.logger.Warn("message handler returned after its message was released", append(fields, zap.Error(err))...)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	if err == nil {
		if cerr := w.q.Complete(ctx, msg.ID, msg.Token); cerr != nil {
			w.logger.Error("message handled, but completing it failed", append(fields, zap.Error(cerr))...)
			return
		}
		w.logger.Info("message handled", fields...)
		return
	}
	cause := err.Error()
	if len(cause) > maxCause {
		cause = cause[:maxCause]
	}
	dead, ferr := w.q.Fail(ctx, msg.ID, msg.Token, cause)
	switch {
	case ferr != nil:
		w.logger.Error("message failed, and recording the failure failed", append(fields, zap.Error(err), zap.NamedError("recordError", ferr))...)
	case dead:
		w.logger.Error("message failed and is dead: its retries are spent", append(fields, zap.Error(err))...)
	default:
		w.logger.Warn("message failed and will be retried", append(fields, zap.Error(err))...)
	}
}

// stop waits up to the grace for the running handlers, then cancels the
// rest and releases their messages.
func (w *loop) stop() error {
	finished := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(finished)
	}()
	if n := w.count(); n > 0 {
		w.logger.Info("stopping: letting the running handlers finish", zap.Int("running", n), zap.Duration("grace", w.opts.Grace))
	}
	select {
	case <-finished:
		w.logger.Info("worker stopped")
		return nil
	case <-time.After(w.opts.Grace):
	}
	w.mu.Lock()
	var unfinished []Message
	for _, h := range w.running {
		h.released = true
		unfinished = append(unfinished, h.msg)
	}
	w.mu.Unlock()
	w.cancelHandlers()
	var released int
	for _, msg := range unfinished {
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		err := w.q.Release(ctx, msg.ID, msg.Token)
		cancel()
		if err != nil {
			w.logger.Error("release failed: the message is due again when its claim expires", zap.String("message", msg.ID), zap.Error(err))
			continue
		}
		released++
	}
	w.logger.Info("worker stopped: the grace is over, and the unfinished messages are released",
		zap.Int("released", released), zap.Int("unfinished", len(unfinished)))
	return nil
}

// sleep waits d, or until ctx ends, and reports whether ctx is still live.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
