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
// Beside the claims, Run prunes the queue (D68): when it starts and every
// five minutes after, it deletes the done messages older than the queue's
// retention, a batch at a time until a batch is short, and logs how many.
// Every instance of every worker of the queue prunes, which is safe: each
// batch skips the rows another holds, and a row deleted once is gone. It
// never deletes a dead message.
//
// Each message's handling is a span of the global tracer, "process
// <queue>", of kind consumer, which the telemetry package's Start makes
// export (D67); its status records the handler's error.
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
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// scope is the instrumentation scope of a message's span.
const scope = "github.com/parable-work/superschematic/runtime/http/go/worker"

// ConcurrencyVariable is the environment variable a platform sets to the
// number of messages a worker handles at a time in its environment: the
// worker's settings or its `@worker` concurrency.
const ConcurrencyVariable = "WORKER_CONCURRENCY"

// The environment variables a platform sets on a worker in drain mode
// (D145; ir.WorkerModeVariable and its neighbours): ModeVariable is
// ModeDrain, IdleSecondsVariable how long the worker waits with no message
// ready or held before it exits, and SlotsVariable how many of it may run
// at once. A worker with no ModeVariable, or ModePool, polls until it is
// stopped.
const (
	ModeVariable        = "WORKER_MODE"
	IdleSecondsVariable = "WORKER_IDLE_SECONDS"
	SlotsVariable       = "WORKER_SLOTS"

	ModePool  = "pool"
	ModeDrain = "drain"

	// DefaultDrainIdle is how long a worker in drain mode waits idle
	// before it exits, unless IdleSecondsVariable says.
	DefaultDrainIdle = 30 * time.Second

	// DrainHorizon is how far ahead a worker in drain mode that found its
	// queue idle looks for a message that is not due yet, a delayed one
	// or a retry after its backoff (D145): it waits for one due within
	// the horizon instead of exiting, and leaves one due later to the
	// platform's next start, a wake or its tick. Five minutes is gcp's
	// default tick, so with that tick a message waits at most about one
	// tick past its due time whether the worker waits for it or not; and
	// a worker kept for it costs at most five minutes of a task that does
	// nothing.
	DrainHorizon = 5 * time.Minute
)

// Drain is how a worker in drain mode runs (D145): it claims as any worker
// does, and Run returns once no message was ready or held for Idle and
// none falls due within Horizon, so the process exits 0 and a queue that
// is empty runs nothing. Slots is how
// many such workers may run at once: each takes one of that many locks
// named for its queue as it starts (Queue.Slot), and one that finds none
// free returns at once, so starts that overlap cost a start and nothing
// else.
type Drain struct {
	// Idle is how long the worker waits with no message ready or held
	// before it stops; zero is DefaultDrainIdle.
	Idle time.Duration

	// Slots is how many workers may drain the queue at once; less than
	// one is one.
	Slots int

	// Horizon is how far ahead the worker looks for a message that is not
	// due yet before it stops (Queue.NextDue); zero is DrainHorizon.
	Horizon time.Duration
}

// DrainFromEnv returns the drain the environment gives a worker, or nil
// for one that runs as a pool's instance: ModeVariable unset, empty or
// ModePool. It refuses a mode it does not know, and an idle time or a
// slot count that is no whole number, one or more.
func DrainFromEnv() (*Drain, error) {
	switch mode := os.Getenv(ModeVariable); mode {
	case "", ModePool:
		return nil, nil
	case ModeDrain:
	default:
		return nil, fmt.Errorf("%s is %q; it is %q or %q", ModeVariable, mode, ModePool, ModeDrain)
	}
	d := &Drain{Idle: DefaultDrainIdle, Slots: 1}
	whole := func(name string) (int, bool, error) {
		value := os.Getenv(name)
		if value == "" {
			return 0, false, nil
		}
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			return 0, false, fmt.Errorf("%s is %q; it is a whole number, one or more", name, value)
		}
		return n, true, nil
	}
	if seconds, ok, err := whole(IdleSecondsVariable); err != nil {
		return nil, err
	} else if ok {
		d.Idle = time.Duration(seconds) * time.Second
	}
	if slots, ok, err := whole(SlotsVariable); err != nil {
		return nil, err
	} else if ok {
		d.Slots = slots
	}
	return d, nil
}

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

	// DefaultPruneEvery is how often a worker prunes its queue's done
	// messages, after the prune it starts with.
	DefaultPruneEvery = 5 * time.Minute

	// PruneBatch is how many done messages one prune statement deletes at
	// most, so no statement holds its locks for long; a prune runs another
	// while one deletes a whole batch.
	PruneBatch = 1000

	// opTimeout bounds each complete, fail, extend, release and prune
	// batch, which run on a context of their own, so a stopping worker
	// still records them.
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
// claims (D53). Every function is required but Prune.
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

	// Prune deletes up to limit messages done for the queue's retention,
	// and returns how many it deleted. Nil prunes nothing.
	Prune func(ctx context.Context, limit int) (int, error)

	// Slot takes one of slots locks named for the queue, which a worker in
	// drain mode holds while it runs (D145): it reports false when every
	// one is taken, and otherwise returns the function that gives the slot
	// back. Nil takes none, as on a database with no such lock: every
	// worker started then drains.
	Slot func(ctx context.Context, slots int) (release func(), ok bool, err error)

	// NextDue reports how long until the first ready message that is not
	// due yet falls due, or false when none waits (D145): a worker in drain
	// mode that found the queue idle waits for one due within its horizon
	// instead of stopping. Nil reads none: the worker stops once idle.
	NextDue func(ctx context.Context) (time.Duration, bool, error)
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

	// PruneEvery is how often Run prunes the queue after the prune it
	// starts with; zero is DefaultPruneEvery.
	PruneEvery time.Duration

	// Logger logs each message's outcome; nil logs nothing.
	Logger *zap.Logger

	// Drain, when set, runs the worker in drain mode (D145): Run takes a
	// slot of the queue, and returns once no message was ready or held for
	// the drain's idle time. Nil polls until ctx ends.
	Drain *Drain

	// quiet is the clock the claim loops of one RunAll share in drain
	// mode, so a behavior worker's queues stop together, once all of them
	// were idle.
	quiet *quiet
}

// quiet is when a worker in drain mode was last busy, and how many of its
// handlers run: a claim that found a message, a handler that started and
// one that returned each make it busy.
type quiet struct {
	last    atomic.Int64
	running atomic.Int64
}

// busy marks the worker busy now.
func (q *quiet) busy() { q.last.Store(time.Now().UnixNano()) }

// over reports whether no handler runs and none was busy for idle.
func (q *quiet) over(idle time.Duration) bool {
	return q.running.Load() == 0 && time.Since(time.Unix(0, q.last.Load())) >= idle
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

// RunAll runs each of queues with Run, all at once, with the same options,
// and returns once every one stopped: the claim loops of a behavior
// worker (D103), one per behavior queue of its table, each up to the
// options' concurrency. It returns the first error of a queue it could
// not run, after the others stopped, which a queue it cannot run makes
// them do at once.
func RunAll(ctx context.Context, queues []Queue, opts Options) error {
	if len(queues) == 0 {
		return errors.New("worker: no queue to run")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if opts.Drain != nil {
		// The queues are idle together or not at all.
		opts.quiet = &quiet{}
		opts.quiet.busy()
	}
	errs := make([]error, len(queues))
	var wg sync.WaitGroup
	for i, q := range queues {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if errs[i] = Run(ctx, q, opts); errs[i] != nil {
				cancel()
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
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
	if opts.PruneEvery <= 0 {
		opts.PruneEvery = DefaultPruneEvery
	}
	logger := opts.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	logger = logger.With(zap.String("queue", q.Name))
	if opts.Drain != nil {
		drain := *opts.Drain
		if drain.Idle <= 0 {
			drain.Idle = DefaultDrainIdle
		}
		if drain.Slots < 1 {
			drain.Slots = 1
		}
		if drain.Horizon <= 0 {
			drain.Horizon = DrainHorizon
		}
		opts.Drain = &drain
		if opts.quiet == nil {
			opts.quiet = &quiet{}
			opts.quiet.busy()
		}
		if q.Slot != nil {
			release, ok, err := q.Slot(ctx, drain.Slots)
			switch {
			case err != nil && ctx.Err() != nil:
				return nil
			case err != nil:
				return fmt.Errorf("worker: take a slot of queue %s: %w", q.Name, err)
			case !ok:
				logger.Info("every slot of the queue is taken: the workers that hold them drain it", zap.Int("slots", drain.Slots))
				return nil
			}
			defer release()
		}
	}
	w := &loop{q: q, opts: opts, logger: logger, running: map[string]*handling{}, done: make(chan struct{}, opts.Concurrency)}
	// The handlers' context outlives ctx by the grace: a stopping worker
	// cancels it only once the grace is over.
	w.handlers, w.cancelHandlers = context.WithCancel(context.WithoutCancel(ctx))
	defer w.cancelHandlers()
	// The prune stops with the claims: when ctx ends, or once a worker in
	// drain mode has drained.
	pruning, stopPruning := context.WithCancel(ctx)
	defer stopPruning()
	pruned := make(chan struct{})
	go func() {
		defer close(pruned)
		w.pruneLoop(pruning)
	}()
	w.claimLoop(ctx)
	stopPruning()
	err := w.stop()
	<-pruned
	return err
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

// claimLoop claims messages for the free slots until ctx ends, or, in
// drain mode, until the worker has drained.
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
		if w.drained(len(msgs)) {
			// A message that falls due within the horizon, a delayed one
			// or a retry, is waited for: the claim after the wait takes it.
			due, ok := w.nextDue(ctx)
			if !ok {
				w.logger.Info("drained: no message was ready or held", zap.Duration("idle", w.opts.Drain.Idle))
				return
			}
			w.logger.Info("idle, and waiting for a message due soon", zap.Duration("dueIn", due))
			if !sleep(ctx, max(due, w.opts.Idle)) {
				return
			}
			continue
		}
		if len(msgs) < free && !w.idle(ctx) {
			return
		}
	}
}

// nextDue reports how long until a message that is not due yet falls due,
// when that is within the drain's horizon: a worker in drain mode that
// found its queue idle waits for it. A queue that reads none, or whose
// read fails, reports false, and the worker stops.
func (w *loop) nextDue(ctx context.Context) (time.Duration, bool) {
	if w.q.NextDue == nil {
		return 0, false
	}
	readCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	due, ok, err := w.q.NextDue(readCtx)
	switch {
	case err != nil:
		if ctx.Err() == nil {
			w.logger.Warn("reading when the next message is due failed: the tick runs it", zap.Error(err))
		}
		return 0, false
	case !ok:
		return 0, false
	case due > w.opts.Drain.Horizon:
		w.logger.Info("the next message is due past the drain's horizon: a wake or the tick runs it",
			zap.Duration("dueIn", due), zap.Duration("horizon", w.opts.Drain.Horizon))
		return 0, false
	}
	return max(due, 0), true
}

// drained reports whether a worker in drain mode is done: its claim found
// nothing, none of its handlers runs, and none was busy for the drain's
// idle time. A claim that found a message makes the worker busy.
func (w *loop) drained(claimed int) bool {
	d, q := w.opts.Drain, w.opts.quiet
	if d == nil || q == nil {
		return false
	}
	if claimed > 0 || w.count() > 0 {
		q.busy()
		return false
	}
	return q.over(d.Idle)
}

// pruneLoop prunes the queue at once, then every PruneEvery, until ctx
// ends.
func (w *loop) pruneLoop(ctx context.Context) {
	if w.q.Prune == nil {
		return
	}
	for {
		w.prune(ctx)
		if !sleep(ctx, w.opts.PruneEvery) {
			return
		}
	}
}

// prune deletes the queue's done messages past its retention, a batch at a
// time until a batch is short, and logs how many it deleted. A failed batch
// ends the prune, which the next tries again.
func (w *loop) prune(ctx context.Context) {
	pruned := 0
	defer func() {
		if pruned > 0 {
			w.logger.Info("done messages pruned", zap.Int("pruned", pruned))
		}
	}()
	for ctx.Err() == nil {
		batchCtx, cancel := context.WithTimeout(ctx, opTimeout)
		n, err := w.q.Prune(batchCtx, PruneBatch)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				w.logger.Warn("pruning the done messages failed", zap.Int("pruned", pruned), zap.Error(err))
			}
			return
		}
		pruned += n
		if n < PruneBatch {
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
	if q := w.opts.quiet; q != nil {
		q.running.Add(1)
		q.busy()
	}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer func() {
			// A handler that returns leaves the worker busy until now.
			if q := w.opts.quiet; q != nil {
				q.busy()
				q.running.Add(-1)
			}
			select {
			case w.done <- struct{}{}:
			default:
			}
		}()
		ctx, span := w.span(msg)
		defer span.End()
		extended := make(chan struct{})
		stopExtending := w.extend(msg, extended)
		err := handle(ctx, msg)
		stopExtending()
		<-extended
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		w.finish(h, err)
	}()
}

// span starts the span of msg's handling, a child of nothing: the
// messaging conventions' process span of the queue (D67).
func (w *loop) span(msg Message) (context.Context, trace.Span) {
	return otel.Tracer(scope).Start(w.handlers, "process "+w.q.Name,
		trace.WithNewRoot(),
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "superschematic"),
			attribute.String("messaging.operation.name", "process"),
			attribute.String("messaging.operation.type", "process"),
			attribute.String("messaging.destination.name", w.q.Name),
			attribute.String("messaging.message.id", msg.ID),
			attribute.Int("superschematic.message.attempt", msg.Attempt),
		),
	)
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
