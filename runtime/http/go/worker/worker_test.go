package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// fakeQueue is a queue in memory: messages to claim, and what the loop did
// with each.
type fakeQueue struct {
	mu        sync.Mutex
	ready     []Message
	claimed   int
	maxBatch  int
	completed []string
	failed    map[string]string
	released  []string
	extended  map[string]int
	claimErr  error
	claimErrs int
}

func (f *fakeQueue) queue(lease time.Duration) Queue {
	return Queue{
		Name:  "Fake",
		Lease: lease,
		Claim: func(_ context.Context, n int, _ time.Duration) ([]Message, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.claimErrs > 0 {
				f.claimErrs--
				return nil, f.claimErr
			}
			f.maxBatch = max(f.maxBatch, n)
			n = min(n, len(f.ready))
			out := f.ready[:n]
			f.ready = f.ready[n:]
			f.claimed += n
			return out, nil
		},
		Extend: func(_ context.Context, id, _ string, _ time.Duration) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.extended[id]++
			return nil
		},
		Complete: func(_ context.Context, id, _ string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.completed = append(f.completed, id)
			return nil
		},
		Fail: func(_ context.Context, id, _, cause string) (bool, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.failed[id] = cause
			return strings.Contains(cause, "fatal"), nil
		},
		Release: func(_ context.Context, id, _ string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.released = append(f.released, id)
			return nil
		},
	}
}

func newFake() *fakeQueue {
	return &fakeQueue{failed: map[string]string{}, extended: map[string]int{}}
}

func (f *fakeQueue) add(id string, handle func(context.Context) error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ready = append(f.ready, Message{ID: id, Token: "token-" + id, Attempt: 1, Handle: handle})
}

// waitFor polls cond until it holds or a few seconds pass.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRunHandlesEachMessage: a handler that returns nil completes its
// message, one that fails or panics fails it, and each outcome is logged.
func TestRunHandlesEachMessage(t *testing.T) {
	f := newFake()
	f.add("ok", func(context.Context) error { return nil })
	f.add("retry", func(context.Context) error { return errors.New("try later") })
	f.add("dead", func(context.Context) error { return errors.New("fatal: no such order") })
	f.add("panics", func(context.Context) error { panic("boom") })
	core, logs := observer.New(zap.InfoLevel)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() {
		stopped <- Run(ctx, f.queue(time.Minute), Options{Concurrency: 2, Idle: 10 * time.Millisecond, Logger: zap.New(core)})
	}()
	waitFor(t, "every message's outcome", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.completed)+len(f.failed) == 4
	})
	cancel()
	if err := <-stopped; err != nil {
		t.Fatalf("Run = %v", err)
	}
	if fmt.Sprint(f.completed) != "[ok]" || f.failed["retry"] != "try later" || f.failed["dead"] != "fatal: no such order" ||
		!strings.HasPrefix(f.failed["panics"], "panic: boom") {
		t.Fatalf("completed %v, failed %v", f.completed, f.failed)
	}
	if f.maxBatch > 2 {
		t.Fatalf("a claim asked for %d messages, more than the concurrency, 2", f.maxBatch)
	}
	for msg, want := range map[string]string{
		"ok":     "message handled",
		"retry":  "message failed and will be retried",
		"dead":   "message failed and is dead: its retries are spent",
		"panics": "message failed and will be retried",
	} {
		found := false
		for _, entry := range logs.All() {
			if entry.Message == want && entry.ContextMap()["message"] == msg && entry.ContextMap()["queue"] == "Fake" {
				found = true
			}
		}
		if !found {
			t.Errorf("no %q logged for %s", want, msg)
		}
	}
}

// TestRunRunsAtMostItsConcurrency: no more handlers run at once than the
// concurrency, and a slot that frees is claimed for.
func TestRunRunsAtMostItsConcurrency(t *testing.T) {
	f := newFake()
	var mu sync.Mutex
	running, most := 0, 0
	for i := range 9 {
		f.add(fmt.Sprint(i), func(context.Context) error {
			mu.Lock()
			running++
			most = max(most, running)
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			running--
			mu.Unlock()
			return nil
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- Run(ctx, f.queue(time.Minute), Options{Concurrency: 3, Idle: 5 * time.Millisecond}) }()
	waitFor(t, "nine messages handled", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.completed) == 9
	})
	cancel()
	<-stopped
	if most != 3 {
		t.Fatalf("at most %d handlers ran at once, want 3", most)
	}
}

// TestRunExtendsALongHandlersClaim: a handler longer than a third of the
// lease has its claim extended.
func TestRunExtendsALongHandlersClaim(t *testing.T) {
	f := newFake()
	f.add("long", func(context.Context) error { time.Sleep(200 * time.Millisecond); return nil })
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- Run(ctx, f.queue(90*time.Millisecond), Options{Idle: 5 * time.Millisecond}) }()
	waitFor(t, "the long message handled", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.completed) == 1
	})
	cancel()
	<-stopped
	if f.extended["long"] < 2 {
		t.Fatalf("the claim was extended %d times, want two or more", f.extended["long"])
	}
}

// TestRunStopsWithinTheGrace: once its context ends, Run claims no more,
// lets a handler that finishes within the grace complete its message,
// cancels the one that does not and releases its message.
func TestRunStopsWithinTheGrace(t *testing.T) {
	f := newFake()
	started := make(chan string, 2)
	f.add("quick", func(context.Context) error {
		started <- "quick"
		time.Sleep(30 * time.Millisecond)
		return nil
	})
	cancelled := make(chan struct{})
	f.add("stuck", func(ctx context.Context) error {
		started <- "stuck"
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() {
		stopped <- Run(ctx, f.queue(time.Minute), Options{Concurrency: 2, Grace: 150 * time.Millisecond, Idle: 5 * time.Millisecond})
	}()
	<-started
	<-started
	f.add("late", func(context.Context) error { return nil })
	began := time.Now()
	cancel()
	if err := <-stopped; err != nil {
		t.Fatalf("Run = %v", err)
	}
	if took := time.Since(began); took < 150*time.Millisecond || took > 2*time.Second {
		t.Fatalf("Run stopped in %s, want about the grace", took)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("the stuck handler's context was not cancelled")
	}
	time.Sleep(20 * time.Millisecond)
	f.mu.Lock()
	defer f.mu.Unlock()
	if fmt.Sprint(f.completed) != "[quick]" || fmt.Sprint(f.released) != "[stuck]" || len(f.failed) != 0 {
		t.Fatalf("completed %v, released %v, failed %v; want quick completed and stuck released", f.completed, f.released, f.failed)
	}
	if len(f.ready) != 1 {
		t.Fatalf("a stopping worker claimed %d more messages", 1-len(f.ready))
	}
}

// TestRunBacksOffAFailedClaim: a claim that fails is logged and tried
// again, after a longer wait each time.
func TestRunBacksOffAFailedClaim(t *testing.T) {
	f := newFake()
	f.claimErr, f.claimErrs = errors.New("the database is down"), 2
	f.add("after", func(context.Context) error { return nil })
	core, logs := observer.New(zap.InfoLevel)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() {
		stopped <- Run(ctx, f.queue(time.Minute), Options{Idle: 5 * time.Millisecond, Logger: zap.New(core)})
	}()
	waitFor(t, "the message handled after the database came back", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.completed) == 1
	})
	cancel()
	<-stopped
	if n := logs.FilterMessage("claim failed").Len(); n != 2 {
		t.Fatalf("logged %d failed claims, want 2", n)
	}
}

// TestConcurrency reads the environment's concurrency, or the default.
func TestConcurrency(t *testing.T) {
	t.Setenv(ConcurrencyVariable, "")
	if n, err := Concurrency(4); n != 4 || err != nil {
		t.Fatalf("Concurrency = %d, %v, want the default", n, err)
	}
	t.Setenv(ConcurrencyVariable, "7")
	if n, err := Concurrency(4); n != 7 || err != nil {
		t.Fatalf("Concurrency = %d, %v, want 7", n, err)
	}
	t.Setenv(ConcurrencyVariable, "0")
	if _, err := Concurrency(4); err == nil {
		t.Fatal("a concurrency of 0 was taken")
	}
}

// TestRunRefusesAnIncompleteQueue: a queue without its functions or lease
// does not run.
func TestRunRefusesAnIncompleteQueue(t *testing.T) {
	if err := Run(context.Background(), Queue{Name: "Fake", Lease: time.Minute}, Options{}); err == nil {
		t.Fatal("a queue without claims ran")
	}
	q := newFake().queue(0)
	if err := Run(context.Background(), q, Options{}); err == nil {
		t.Fatal("a queue without a lease ran")
	}
}
