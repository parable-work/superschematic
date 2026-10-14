// Package engine is the Go binding of the engine (D16, D58): the Rust
// engine, linked as the static archive libsuperschematic_engine.a through
// its C ABI (runtime/engine/rust/ffi, include/superschematic_engine.h).
//
// Open opens an engine on SQLite or Postgres; Call makes any of the
// engine's calls by name with JSON arguments, as the TypeScript engine's
// methods take them; Deploy defines and publishes a server's schema
// documents; Health and Ready are what a server's /readyz reads; Runner
// drives the runner of behaviors' reactions and schedules; Close shuts it
// down. Package enginehttp mounts the engine's HTTP API, event stream and
// MCP endpoint on a chi router.
//
// Every call is a blocking call into the archive, on a thread of its own
// while it runs. The binding makes at most Config.Concurrency at once and
// the rest wait, each for its context; a call that has started cannot be
// cancelled, and runs to its end.
//
// The package links libsuperschematic_engine.a through cgo. Build it with
// scripts/engine-archive.sh, which stages it under lib/<goos>_<goarch>;
// outside this checkout, put the directory that holds it in CGO_LDFLAGS as
// -L<dir> (a release's archives tarball holds it beside superscalar's and
// the version graph's, all three built with one Rust release, so they link
// into one binary). A project whose schemas compose an extension's
// behaviors links its own archive in its place
// (scripts/engine-archive.sh --crate).
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// Engine is an open engine. Its methods are safe to call from many
// goroutines.
type Engine struct {
	handle uint64
	info   Info
	slots  chan struct{}
	closed atomic.Bool

	// The waiter: one goroutine blocked in the archive until a commit,
	// which wakes every stream of the engine (Changed).
	mu      sync.Mutex
	changed chan struct{}
	waiting bool
	// ended is set once the waiter found the engine closed.
	ended bool
}

// Info is what an engine reports of itself when it opens.
type Info struct {
	// ABI is the version of the C ABI the archive speaks.
	ABI uint32 `json:"abi"`
	// Project is the crate that built the archive: superschematic-engine-ffi
	// for the core's.
	Project string `json:"project"`
	// Dialect is sqlite or postgres.
	Dialect string `json:"dialect"`
	// Behaviors are the behaviors the engine runs, by name.
	Behaviors []string `json:"behaviors"`
}

// Open opens an engine on its config: its storage, created if absent, is
// migrated, and the archive's behaviors are registered.
func Open(cfg Config) (*Engine, error) {
	config, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("engine: write the config: %w", err)
	}
	return OpenJSON(config, cfg.concurrency())
}

// OpenJSON opens an engine on its config as JSON (runtime/engine/rust/ffi
// documents its members), making at most concurrency calls at once (8
// when it is 0 or less).
func OpenJSON(config []byte, concurrency int) (*Engine, error) {
	if version := abiVersion(); version != ABIVersion {
		return nil, fmt.Errorf("engine: the linked archive speaks C ABI %d and this binding %d: rebuild it with scripts/engine-archive.sh", version, ABIVersion)
	}
	if concurrency <= 0 {
		concurrency = 8
	}
	handle, out, c := engOpen(config)
	if c != codeOK {
		return nil, failure(c, out)
	}
	e := &Engine{handle: handle, slots: make(chan struct{}, concurrency), changed: make(chan struct{})}
	if err := json.Unmarshal(out, &e.info); err != nil {
		engClose(handle)
		return nil, fmt.Errorf("engine: read what the archive opened: %w", err)
	}
	return e, nil
}

// Info is what the engine reported when it opened.
func (e *Engine) Info() Info {
	return e.info
}

// acquire takes a slot for a call, waiting for ctx.
func (e *Engine) acquire(ctx context.Context) error {
	if e.closed.Load() {
		return ErrClosed
	}
	select {
	case e.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Engine) release() {
	<-e.slots
}

// Request is one call: its name, the principal it acts for, its arguments
// after the principal, and, for an engine on a manual clock, the time to
// set the clock to first.
type Request struct {
	Op        string            `json:"op"`
	Principal *Principal        `json:"principal,omitempty"`
	Args      []json.RawMessage `json:"args,omitempty"`
	Now       *int64            `json:"now,omitempty"`
}

// Do makes a call and returns its result as JSON: nil where the TypeScript
// method returns undefined.
func (e *Engine) Do(ctx context.Context, request Request) (json.RawMessage, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("engine: write the call %s: %w", request.Op, err)
	}
	if err := e.acquire(ctx); err != nil {
		return nil, err
	}
	out, c := engCall(e.handle, body)
	e.release()
	if c != codeOK {
		return nil, failure(c, out)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return json.RawMessage(out), nil
}

// Call makes the call op (instances.create, schemas.publish, ...) as
// principal, nil for a call that takes none, with its arguments after the
// principal, each written as JSON (a json.RawMessage as it is). It returns
// the result as JSON, nil for none.
func (e *Engine) Call(ctx context.Context, op string, principal *Principal, args ...any) (json.RawMessage, error) {
	written := make([]json.RawMessage, len(args))
	for i, arg := range args {
		switch given := arg.(type) {
		case json.RawMessage:
			written[i] = given
		default:
			text, err := json.Marshal(arg)
			if err != nil {
				return nil, fmt.Errorf("engine: write argument %d of %s: %w", i+1, op, err)
			}
			written[i] = text
		}
	}
	return e.Do(ctx, Request{Op: op, Principal: principal, Args: written})
}

// DeployOptions is where Deploy publishes.
type DeployOptions struct {
	// Namespace is the namespace the documents are published in: default
	// unless set.
	Namespace string `json:"namespace,omitempty"`
}

// Deployed is what Deploy did with one document.
type Deployed struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// Version is the live version after the deploy.
	Version int64 `json:"version"`
	// Defined is false when the live version had the document's hash, and
	// nothing was written.
	Defined bool `json:"defined"`
	// Published is true when the deploy minted a version.
	Published bool `json:"published"`
}

// Deploy defines and publishes a server's schema documents, in order, as
// the engine's own principal, which the policy allows everything and no
// request can be: a document whose live version has its hash is left as
// it is, so a restart with the same documents writes nothing, and a
// changed one becomes the next version if the compatibility rule allows
// it. A server deploys before it reports ready.
func (e *Engine) Deploy(ctx context.Context, documents []json.RawMessage, options DeployOptions) ([]Deployed, error) {
	if documents == nil {
		documents = []json.RawMessage{}
	}
	out, err := e.Call(ctx, "deploy", nil, documents, options)
	if err != nil {
		return nil, err
	}
	var deployed []Deployed
	if err := json.Unmarshal(out, &deployed); err != nil {
		return nil, fmt.Errorf("engine: read the deploy: %w", err)
	}
	return deployed, nil
}

// Health is the engine's state as a server's readiness reads it.
type Health struct {
	// Ready is false once the engine closed, after a panic inside a
	// transaction poisoned it, or while its database does not answer a
	// read.
	Ready    bool   `json:"ready"`
	Closed   bool   `json:"closed"`
	Poisoned bool   `json:"poisoned"`
	Dialect  string `json:"dialect"`
	// Listening is whether a Postgres listener is connected, so a commit
	// in another process wakes this one.
	Listening bool `json:"listening"`
	// Error is why the database did not answer.
	Error string `json:"error,omitempty"`
}

// Health reads the engine's state.
func (e *Engine) Health(ctx context.Context) (Health, error) {
	out, err := e.Call(ctx, "health", nil)
	if errors.Is(err, ErrClosed) {
		return Health{Closed: true, Dialect: e.info.Dialect}, nil
	}
	if err != nil {
		return Health{}, err
	}
	var health Health
	if err := json.Unmarshal(out, &health); err != nil {
		return Health{}, fmt.Errorf("engine: read the health: %w", err)
	}
	return health, nil
}

// Ready is nil while the engine is ready (Health), else why not: what a
// server's /readyz checks.
func (e *Engine) Ready(ctx context.Context) error {
	health, err := e.Health(ctx)
	switch {
	case err != nil:
		return err
	case health.Ready:
		return nil
	case health.Closed:
		return ErrClosed
	case health.Poisoned:
		return errors.New("engine: a panic inside a transaction poisoned the engine; restart it")
	default:
		return fmt.Errorf("engine: the database does not answer: %s", health.Error)
	}
}

// Runner drives the runner of behaviors' reactions and schedules, and of
// retention.
func (e *Engine) Runner() *RunnerControl {
	return &RunnerControl{e: e}
}

// RunnerControl starts and stops the runner's thread, or runs a pass.
type RunnerControl struct {
	e *Engine
}

func (r *RunnerControl) run(ctx context.Context, action string) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]string{"action": action})
	if err := r.e.acquire(ctx); err != nil {
		return nil, err
	}
	out, c := engRunner(r.e.handle, body)
	r.e.release()
	if c != codeOK {
		return nil, failure(c, out)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return json.RawMessage(out), nil
}

// Start starts the runner's own thread, which runs what is due and waits
// for commits; on Postgres only the process that holds the runner's lock
// runs.
func (r *RunnerControl) Start(ctx context.Context) error {
	_, err := r.run(ctx, "start")
	return err
}

// Stop stops the runner's thread.
func (r *RunnerControl) Stop(ctx context.Context) error {
	_, err := r.run(ctx, "stop")
	return err
}

// RunDue runs one pass of what is due, on this goroutine, and returns it.
func (r *RunnerControl) RunDue(ctx context.Context) (json.RawMessage, error) {
	return r.run(ctx, "run_due")
}

// Status is the runner's state: its subscriptions, schedules and
// retention.
func (r *RunnerControl) Status(ctx context.Context) (json.RawMessage, error) {
	return r.run(ctx, "status")
}

// Close shuts the engine down: its runner stops, every event stream ends,
// and every later call is ErrClosed. Its storage closes once the calls in
// flight return. Closing a closed engine does nothing.
func (e *Engine) Close() error {
	if e.closed.Swap(true) {
		return nil
	}
	if c := engClose(e.handle); c != codeOK && c != codeClosed {
		return fmt.Errorf("engine: close: the archive returned %d", c)
	}
	return nil
}

// Changed returns a channel closed at the next commit (in any process, on
// Postgres) or when the engine closes: one goroutine waits in the archive
// and wakes every stream. Take it before reading, so a commit between the
// read and the wait is not missed.
func (e *Engine) Changed() <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	ch := e.changed
	if !e.waiting && !e.ended {
		e.waiting = true
		go e.wait()
	}
	return ch
}

// wait blocks in the archive until the epoch moves, then wakes every
// waiter and waits again, until the engine closes.
func (e *Engine) wait() {
	epoch, c := engWait(e.handle, 0, 0)
	for c == codeOK {
		var moved uint64
		moved, c = engWait(e.handle, epoch, 1000)
		if c == codeOK && moved != epoch {
			epoch = moved
			e.wake(false, false)
		}
	}
	e.wake(true, c == codeClosed)
}

// wake closes the channel Changed returned and makes the next one. The
// waiter's last wake leaves a closed channel once the engine is closed, so
// every later Changed returns at once; after a failure it leaves an open
// one, and the next Changed starts another waiter.
func (e *Engine) wake(last, closed bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	close(e.changed)
	e.changed = make(chan struct{})
	if last {
		e.waiting = false
		if closed {
			e.ended = true
			close(e.changed)
		}
	}
}
