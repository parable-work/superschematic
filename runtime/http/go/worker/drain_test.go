package worker

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// slots is the locks of one queue, as a database keeps them: Slot takes
// one of as many as it is asked for, and reports false when all are held.
type slots struct {
	mu    sync.Mutex
	held  int
	asked []int
	err   error
}

func (s *slots) slot(_ context.Context, n int) (func(), bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, n)
	if s.err != nil {
		return nil, false, s.err
	}
	if s.held >= n {
		return nil, false, nil
	}
	s.held++
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.held--
	}, true, nil
}

func (s *slots) holds() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held
}

// TestADrainWorkerExitsOnceItsQueueIsIdle (D145): a worker in drain mode
// handles what its queue holds, stays up while a handler holds a message
// however long that is, and returns nil once no message was ready or held
// for its idle time, which it logs. Nothing cancelled it.
func TestADrainWorkerExitsOnceItsQueueIsIdle(t *testing.T) {
	const idle = 80 * time.Millisecond
	f := newFake()
	gate := make(chan struct{})
	f.add("held", func(context.Context) error { <-gate; return nil })
	f.add("ok", func(context.Context) error { return nil })
	core, logs := observer.New(zap.InfoLevel)
	stopped := make(chan error, 1)
	go func() {
		stopped <- Run(context.Background(), f.queue(time.Minute), Options{Concurrency: 2, Idle: 5 * time.Millisecond, Logger: zap.New(core), Drain: &Drain{Idle: idle}})
	}()
	waitFor(t, "the message that finishes", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.completed) == 1
	})
	// The held message keeps the worker up past its idle time.
	select {
	case err := <-stopped:
		t.Fatalf("the worker exited while a handler held a message: %v", err)
	case <-time.After(3 * idle):
	}
	let := time.Now()
	close(gate)
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not exit once its queue was idle")
	}
	if waited := time.Since(let); waited < idle {
		t.Errorf("the worker exited %s after its last handler returned, before its idle time, %s", waited, idle)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.completed) != 2 || len(f.released) != 0 {
		t.Errorf("completed %v, released %v", f.completed, f.released)
	}
	if logs.FilterMessage("drained: no message was ready or held").Len() != 1 {
		t.Errorf("the worker did not log that it drained: %v", logs.All())
	}
}

// TestADrainWorkerKeepsClaimingWhileMessagesCome: a message that arrives
// within the idle time is handled, and the idle time starts again after it.
func TestADrainWorkerKeepsClaimingWhileMessagesCome(t *testing.T) {
	const idle = 400 * time.Millisecond
	f := newFake()
	stopped := make(chan error, 1)
	started := time.Now()
	go func() {
		stopped <- Run(context.Background(), f.queue(time.Minute), Options{Idle: 5 * time.Millisecond, Drain: &Drain{Idle: idle}})
	}()
	for i, id := range []string{"a", "b", "c"} {
		time.Sleep(idle / 4)
		f.add(id, func(context.Context) error { return nil })
		waitFor(t, "message "+id, func() bool {
			f.mu.Lock()
			defer f.mu.Unlock()
			return len(f.completed) == i+1
		})
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not exit")
	}
	if ran := time.Since(started); ran < 3*idle/4+idle {
		t.Errorf("the worker ran %s; three messages a quarter of an idle time apart and the idle time after the last are %s or more", ran, 3*idle/4+idle)
	}
}

// TestADrainWorkerTakesASlotOrExitsAtOnce (D145): a worker in drain mode
// takes one of its queue's slots before it claims and gives it back when
// it exits; one that finds every slot taken claims nothing and returns nil
// at once, so starts that overlap cost a start and nothing else; and with
// two slots two run at once.
func TestADrainWorkerTakesASlotOrExitsAtOnce(t *testing.T) {
	locks := &slots{}
	opts := func(n int) Options {
		return Options{Idle: 5 * time.Millisecond, Drain: &Drain{Idle: 40 * time.Millisecond, Slots: n}}
	}
	run := func(f *fakeQueue, n int) chan error {
		q := f.queue(time.Minute)
		q.Slot = locks.slot
		done := make(chan error, 1)
		go func() { done <- Run(context.Background(), q, opts(n)) }()
		return done
	}
	first, gate := newFake(), make(chan struct{})
	first.add("m", func(context.Context) error { <-gate; return nil })
	firstDone := run(first, 1)
	waitFor(t, "the first worker's claim", func() bool {
		first.mu.Lock()
		defer first.mu.Unlock()
		return first.claimed == 1
	})

	second := newFake()
	second.add("n", func(context.Context) error { return nil })
	core, logs := observer.New(zap.InfoLevel)
	q := second.queue(time.Minute)
	q.Slot = locks.slot
	// Its idle time is long, so only an exit at once returns in time.
	o := Options{Idle: 5 * time.Millisecond, Logger: zap.New(core), Drain: &Drain{Idle: time.Minute, Slots: 1}}
	began := time.Now()
	if err := Run(context.Background(), q, o); err != nil {
		t.Fatalf("the worker that found no slot: Run = %v", err)
	}
	if took := time.Since(began); took > 10*time.Second {
		t.Errorf("the worker that found no slot took %s to exit; it exits at once, with no idle time", took)
	}
	if second.claimed != 0 {
		t.Errorf("the worker that found no slot claimed %d messages", second.claimed)
	}
	if logs.FilterMessage("every slot of the queue is taken: the workers that hold them drain it").Len() != 1 {
		t.Errorf("the worker that found no slot logged %v", logs.All())
	}

	// With two slots, a second worker runs beside the first.
	third := newFake()
	third.add("o", func(context.Context) error { return nil })
	thirdDone := run(third, 2)
	waitFor(t, "the third worker's message", func() bool {
		third.mu.Lock()
		defer third.mu.Unlock()
		return len(third.completed) == 1
	})
	if got := locks.holds(); got != 2 {
		t.Errorf("%d slots are held while two workers run", got)
	}
	close(gate)
	for _, done := range []chan error{firstDone, thirdDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Run = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a worker did not exit")
		}
	}
	if got := locks.holds(); got != 0 {
		t.Errorf("%d slots are held after every worker exited", got)
	}
	if want := []int{1, 1, 2}; len(locks.asked) != 3 || locks.asked[0] != want[0] || locks.asked[1] != want[1] || locks.asked[2] != want[2] {
		t.Errorf("the workers asked for one of %v slots, want %v", locks.asked, want)
	}

	// A slot that cannot be asked for is the worker's error: it does not
	// drain unguarded.
	broken := &slots{err: errors.New("the database is down")}
	bq := newFake().queue(time.Minute)
	bq.Slot = broken.slot
	if err := Run(context.Background(), bq, opts(1)); err == nil || !strings.Contains(err.Error(), "take a slot of queue Fake: the database is down") {
		t.Errorf("Run with a slot that fails = %v", err)
	}

	// A pool's worker takes none.
	pool := &slots{}
	pq := newFake().queue(time.Minute)
	pq.Slot = pool.slot
	ctx, cancel := context.WithCancel(context.Background())
	poolDone := make(chan error, 1)
	go func() { poolDone <- Run(ctx, pq, Options{Idle: 5 * time.Millisecond}) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	if err := <-poolDone; err != nil || len(pool.asked) != 0 {
		t.Errorf("a pool's worker: Run = %v, slots asked for %v", err, pool.asked)
	}
}

// TestRunAllDrainsItsQueuesTogether: the claim loops of one RunAll share
// one idle clock in drain mode, so a queue that is empty stays up while
// another's handler runs, and all stop once all were idle.
func TestRunAllDrainsItsQueuesTogether(t *testing.T) {
	const idle = 60 * time.Millisecond
	quiet, busy := newFake(), newFake()
	gate := make(chan struct{})
	busy.add("held", func(context.Context) error { <-gate; return nil })
	stopped := make(chan error, 1)
	go func() {
		stopped <- RunAll(context.Background(), []Queue{quiet.queue(time.Minute), busy.queue(time.Minute)}, Options{Idle: 5 * time.Millisecond, Drain: &Drain{Idle: idle}})
	}()
	time.Sleep(3 * idle)
	// The empty queue is still claimed from: a message for it is handled.
	quiet.add("late", func(context.Context) error { return nil })
	waitFor(t, "the message of the queue that was empty", func() bool {
		quiet.mu.Lock()
		defer quiet.mu.Unlock()
		return len(quiet.completed) == 1
	})
	close(gate)
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("RunAll = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunAll did not return once its queues were idle")
	}
}

// TestDrainFromEnv: the platform's variables give the drain, with the
// defaults for those it leaves out; no mode, or pool, gives none; and a
// mode, an idle time or a slot count that cannot be read is refused.
func TestDrainFromEnv(t *testing.T) {
	for _, mode := range []string{"", ModePool} {
		t.Setenv(ModeVariable, mode)
		if d, err := DrainFromEnv(); d != nil || err != nil {
			t.Errorf("DrainFromEnv with mode %q = %v, %v", mode, d, err)
		}
	}
	t.Setenv(ModeVariable, ModeDrain)
	if d, err := DrainFromEnv(); err != nil || d == nil || d.Idle != DefaultDrainIdle || d.Slots != 1 {
		t.Errorf("DrainFromEnv with the mode alone = %+v, %v", d, err)
	}
	t.Setenv(IdleSecondsVariable, "45")
	t.Setenv(SlotsVariable, "3")
	if d, err := DrainFromEnv(); err != nil || d == nil || d.Idle != 45*time.Second || d.Slots != 3 {
		t.Errorf("DrainFromEnv = %+v, %v", d, err)
	}
	for name, value := range map[string]string{IdleSecondsVariable: "soon", SlotsVariable: "0"} {
		t.Setenv(IdleSecondsVariable, "45")
		t.Setenv(SlotsVariable, "3")
		t.Setenv(name, value)
		if _, err := DrainFromEnv(); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("DrainFromEnv with %s=%s = %v", name, value, err)
		}
	}
	t.Setenv(ModeVariable, "burst")
	if _, err := DrainFromEnv(); err == nil || !strings.Contains(err.Error(), `WORKER_MODE is "burst"`) {
		t.Errorf("DrainFromEnv with an unknown mode = %v", err)
	}
}

// TestADrainWorkerWaitsForAMessageDueWithinItsHorizon (D145): a worker in
// drain mode that found its queue idle reads when the next message falls
// due, a delayed one or a retry, waits for one due within its horizon and
// handles it, and stops once the next is due past the horizon; it logs
// both. One whose due time cannot be read, or reads none, stops once
// idle.
func TestADrainWorkerWaitsForAMessageDueWithinItsHorizon(t *testing.T) {
	f := newFake()
	dueAt := time.Now().Add(150 * time.Millisecond)
	var later bool
	time.AfterFunc(150*time.Millisecond, func() {
		f.add("delayed", func(context.Context) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			// The next message is due in an hour, past the horizon.
			later = true
			return nil
		})
	})
	q := f.queue(time.Minute)
	q.NextDue = func(context.Context) (time.Duration, bool, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case later:
			return time.Hour, true, nil
		case len(f.ready) > 0:
			return 0, false, nil
		}
		return max(time.Until(dueAt), 0), true, nil
	}
	core, logs := observer.New(zap.InfoLevel)
	started := time.Now()
	if err := Run(context.Background(), q, Options{Idle: 5 * time.Millisecond, Logger: zap.New(core), Drain: &Drain{Idle: 20 * time.Millisecond, Horizon: time.Minute}}); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if took := time.Since(started); took < 145*time.Millisecond {
		t.Errorf("the worker stopped after %s, before the delayed message was due", took)
	}
	f.mu.Lock()
	completed := f.completed
	f.mu.Unlock()
	if len(completed) != 1 || completed[0] != "delayed" {
		t.Errorf("completed %v, want the delayed message", completed)
	}
	for _, msg := range []string{"idle, and waiting for a message due soon", "the next message is due past the drain's horizon: a wake or the tick runs it", "drained: no message was ready or held"} {
		if n := logs.FilterMessage(msg).Len(); n == 0 {
			t.Errorf("no %q logged", msg)
		}
	}

	for name, next := range map[string]func(context.Context) (time.Duration, bool, error){
		"none": func(context.Context) (time.Duration, bool, error) { return 0, false, nil },
		"past": func(context.Context) (time.Duration, bool, error) { return 10 * time.Minute, true, nil },
		"a failed read": func(context.Context) (time.Duration, bool, error) {
			return 0, false, errors.New("the database is down")
		},
		"no NextDue set": nil,
	} {
		q := newFake().queue(time.Minute)
		q.NextDue = next
		started := time.Now()
		if err := Run(context.Background(), q, Options{Idle: 5 * time.Millisecond, Drain: &Drain{Idle: 20 * time.Millisecond}}); err != nil {
			t.Errorf("%s: Run = %v", name, err)
		}
		if took := time.Since(started); took > 5*time.Second {
			t.Errorf("%s: the worker took %s to stop", name, took)
		}
	}
}
