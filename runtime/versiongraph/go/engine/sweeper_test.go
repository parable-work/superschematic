package engine_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/runtime/versiongraph/go/engine"
)

// pass is one sweeper pass as RunSweeper reported it.
type pass struct {
	report *engine.SweepReport
	err    error
}

// TestRunSweeperSkipsWhileTheLockIsHeld runs the sweeper while another
// transaction holds the graph's sweep lock: its passes are skipped until the
// lock is released, the next pass sweeps, and cancelling the context stops
// it with the context's error.
func TestRunSweeperSkipsWhileTheLockIsHeld(t *testing.T) {
	dsn := os.Getenv(databaseVariable)
	if dsn == "" {
		t.Skip("set " + databaseVariable + " to run the sweeper against Postgres")
	}
	createSQL, err := os.ReadFile(filepath.Join(fixtureDir, "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	r := newRunner(t, dsn, mustReadFixture(t), createSQL)
	r.holdSweepLock(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	passes := make(chan pass, 1024)
	done := make(chan error, 1)
	go func() {
		done <- r.engine.RunSweeper(ctx, 5*time.Millisecond, engine.SweepOptions{Actor: defaultActor}, func(report *engine.SweepReport, err error) {
			passes <- pass{report, err}
		})
	}()
	next := func() pass {
		t.Helper()
		select {
		case p := <-passes:
			if p.err != nil {
				t.Fatalf("a pass failed: %v", p.err)
			}
			return p
		case <-time.After(10 * time.Second):
			t.Fatal("no pass within 10s")
			return pass{}
		}
	}
	for i := 0; i < 3; i++ {
		if p := next(); !p.report.Skipped {
			t.Fatalf("pass %d swept while another transaction held the sweep lock: %+v", i, p.report)
		}
	}
	if err := r.holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	r.holder = nil
	for {
		if p := next(); !p.report.Skipped {
			break
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunSweeper returned %v, want the context's error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunSweeper did not return after its context was cancelled")
	}
}

func TestRunSweeperRefusesAnIntervalThatIsNotPositive(t *testing.T) {
	eng, err := engine.New(mustReadFixture(t), nil, engine.Options{})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if err := eng.RunSweeper(context.Background(), 0, engine.SweepOptions{Actor: defaultActor}, func(*engine.SweepReport, error) { called = true }); err == nil {
		t.Fatal("RunSweeper took a zero interval")
	}
	if called {
		t.Fatal("RunSweeper ran a pass with a zero interval")
	}
}

func mustReadFixture(t *testing.T) []byte {
	t.Helper()
	descriptor, err := os.ReadFile(filepath.Join(fixtureDir, "recipe.json"))
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}
