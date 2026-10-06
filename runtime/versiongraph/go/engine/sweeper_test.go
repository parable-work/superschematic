package engine_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/parable-work/superschematic/runtime/versiongraph/go/engine"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/internal/testdb"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/postgres"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/storage"
)

// pass is one sweeper pass as RunSweeper reported it.
type pass struct {
	report *engine.SweepReport
	err    error
}

// TestRunSweeperSkipsWhileTheLockIsHeld runs the sweeper while another
// transaction holds the graph's sweep lock: its passes are skipped until the
// lock is released, the next pass sweeps, and cancelling the context stops
// it with the context's error. It runs in a database of its own (package
// testdb), where no other test's lock or sweep reaches it.
func TestRunSweeperSkipsWhileTheLockIsHeld(t *testing.T) {
	dsn := os.Getenv(databaseVariable)
	if dsn == "" {
		t.Skip("set " + databaseVariable + " to run the sweeper against Postgres")
	}
	createSQL, err := os.ReadFile(filepath.Join(fixtureDir, "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	r := newRunner(t, testdb.New(t, dsn), mustReadFixture(t), createSQL)
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

// TestSweepSkipsAnIdleDraftWrittenDuringThePass: a write lands on an idle
// change set after the pass read it as idle and before the pass discards
// it. The pass leaves that change set live, since it is no longer idle,
// and the rest of the pass lands: it discards the other idle change set
// and collects a discarded ref's rows. It runs in a database of its own
// (package testdb), where no other test's lock makes its pass skip.
func TestSweepSkipsAnIdleDraftWrittenDuringThePass(t *testing.T) {
	dsn := os.Getenv(databaseVariable)
	if dsn == "" {
		t.Skip("set " + databaseVariable + " to run the sweeper against Postgres")
	}
	createSQL, err := os.ReadFile(filepath.Join(fixtureDir, "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	r := newRunner(t, testdb.New(t, dsn), mustReadFixture(t), createSQL)
	ctx := context.Background()
	check := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	_, err = r.conn.Exec(ctx, "INSERT INTO recipe (id, title, created_by) VALUES ($1::uuid, 'Bread', $2::uuid)", hyphenated(t, "Bread"), hyphenated(t, defaultActor))
	check("insert the root", err)
	main, err := r.engine.CreatePrimary(ctx, defaultActor, "Bread", "main")
	check("create the primary line", err)
	branch := func(name string) storage.Ref {
		t.Helper()
		ref, err := r.engine.Branch(ctx, defaultActor, main.ID, name)
		check("branch "+name, err)
		return ref
	}
	idle, busy, dropped := branch("idle"), branch("busy"), branch("dropped")
	mix := engine.Edits{"step": {Upsert: []json.RawMessage{json.RawMessage(`{"entity_key": "Mix", "position": 1, "instruction": "Mix", "timings": {}}`)}}}
	saved, err := r.engine.Save(ctx, defaultActor, dropped.ID, dropped.Version, mix)
	check("save on dropped", err)
	check("discard dropped", r.engine.Discard(ctx, defaultActor, dropped.ID, saved.Ref.Version))
	_, err = r.conn.Exec(ctx, "UPDATE recipe_ref SET updated_at = now() - interval '3 days' WHERE id IN ($1::uuid, $2::uuid)", hyphenated(t, idle.ID), hyphenated(t, busy.ID))
	check("age the change sets", err)
	_, err = r.conn.Exec(ctx, "UPDATE recipe_ref SET deleted_at = now() - interval '8 days' WHERE id = $1::uuid", hyphenated(t, dropped.ID))
	check("age the discard", err)

	// The write goes through another connection, in a transaction of its
	// own that commits while the pass's is open.
	other, err := pgx.ConnectConfig(ctx, r.config)
	check("connect", err)
	t.Cleanup(func() { _ = other.Close(context.Background()) })
	otherStore := r.adapter.Storage(postgres.Pgx(other))
	writer := r.engine.WithStorage(otherStore)
	var wrote *engine.SaveResult
	racing := racingStorage{Storage: r.store, afterIdleDrafts: func(ctx context.Context) error {
		var ref storage.Ref
		err := otherStore.Transact(ctx, func(ctx context.Context, tx storage.Tx) error {
			var err error
			ref, err = tx.ReadRef(ctx, busy.ID)
			return err
		})
		if err != nil {
			return err
		}
		wrote, err = writer.Save(ctx, defaultActor, busy.ID, ref.Version, mix)
		return err
	}}
	report, err := r.engine.WithStorage(racing).Sweep(ctx, engine.SweepOptions{Actor: defaultActor, AbandonAfter: 48 * time.Hour})
	check("sweep", err)
	if wrote == nil {
		t.Fatal("the pass did not read the idle change sets")
	}
	if report.Abandoned != 1 || report.CollectedRefs != 1 || report.CollectedRows["step"] != 1 {
		t.Fatalf("report %+v, want one change set abandoned and the discarded ref's one row collected", report)
	}
	if _, err := r.engine.Compose(ctx, idle.ID); !errors.Is(err, engine.ErrNotFound) {
		t.Fatalf("compose the idle change set = %v, want it discarded", err)
	}
	tree, err := r.engine.Compose(ctx, busy.ID)
	check("compose the written change set", err)
	if len(tree.Tree["step"]) != 1 {
		t.Fatalf("the written change set composes to %s, want its step", mustJSON(t, tree.Tree))
	}
}

// racingStorage runs afterIdleDrafts in a pass's transaction as soon as
// the pass has read the idle change sets.
type racingStorage struct {
	storage.Storage
	afterIdleDrafts func(ctx context.Context) error
}

func (s racingStorage) Transact(ctx context.Context, fn func(ctx context.Context, tx storage.Tx) error) error {
	return s.Storage.Transact(ctx, func(ctx context.Context, tx storage.Tx) error {
		return fn(ctx, racingTx{Tx: tx, after: s.afterIdleDrafts})
	})
}

type racingTx struct {
	storage.Tx
	after func(ctx context.Context) error
}

func (t racingTx) IdleDrafts(ctx context.Context, idle time.Duration) ([]storage.Ref, error) {
	refs, err := t.Tx.IdleDrafts(ctx, idle)
	if err != nil {
		return nil, err
	}
	return refs, t.after(ctx)
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
