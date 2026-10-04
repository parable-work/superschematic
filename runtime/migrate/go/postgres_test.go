package migrate_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/internal/testdb"
)

// TestPostgresStepOutsideATransactionRecovers: a CREATE UNIQUE INDEX
// CONCURRENTLY that fails leaves an invalid index and a log row without
// finished_at; the next apply runs the step's recovery, which drops the
// index, before it builds it again. Without the recovery the build fails on
// the index the first one left.
func TestPostgresStepOutsideATransactionRecovers(t *testing.T) {
	for _, withRecovery := range []bool{true, false} {
		name := "with its recovery"
		if !withRecovery {
			name = "without a recovery"
		}
		t.Run(name, func(t *testing.T) {
			url := testdb.Postgres(t)
			r := newRunner(t, url)
			apply(t, r, plan(t, migrate.Postgres, "01-create"), migrate.All)
			seed(t, url)
			testdb.Exec(t, url, `UPDATE "order" SET total = 100 WHERE id = 2`)
			unique := plan(t, migrate.Postgres, "unique-index")
			if !withRecovery {
				unique = edited(t, migrate.Postgres, "unique-index", func(p map[string]any) {
					delete(steps(p)[0].(map[string]any), "recovery")
				})
			}

			_, err := r.Apply(context.Background(), unique, migrate.All)
			var stepErr *migrate.StepError
			if !errors.As(err, &stepErr) || stepErr.Index != 1 || !strings.Contains(stepErr.Statement, "CREATE UNIQUE INDEX CONCURRENTLY") {
				t.Fatalf("apply over duplicate totals = %v", err)
			}
			if got := testdb.Strings(t, url, `SELECT indisvalid::text FROM pg_index WHERE indexrelid = 'order_total_key'::regclass`); len(got) != 1 || got[0] != "false" {
				t.Fatalf("the failed build left %v, want an invalid index", got)
			}
			st := status(t, r, "shop")
			if st.PlanHash != unique.Hash || len(st.Steps) != 1 || st.Steps[0].FinishedAt != "" || st.Steps[0].StartedAt == "" {
				t.Fatalf("status after the failure: %+v", st)
			}

			testdb.Exec(t, url, `UPDATE "order" SET total = 250 WHERE id = 2`)
			result, err := r.Apply(context.Background(), unique, migrate.All)
			if !withRecovery {
				if err == nil || !strings.Contains(err.Error(), "already exists") {
					t.Fatalf("apply without a recovery = %v, want the build to fail on the invalid index", err)
				}
				return
			}
			if err != nil || !equalInts(result.Ran, []int{1}) || !result.Finished {
				t.Fatalf("apply after fixing the data = %+v, %v", result, err)
			}
			if !r.Log.(*testLog).contains("running its recovery") {
				t.Fatal("the runner did not say it ran the recovery")
			}
			if got := testdb.Strings(t, url, `SELECT indisvalid::text FROM pg_index WHERE indexrelid = 'order_total_key'::regclass`); len(got) != 1 || got[0] != "true" {
				t.Fatalf("index after recovery: %v", got)
			}
			if st := status(t, r, "shop"); st.ModelHash != unique.To || st.PlanHash != "" {
				t.Fatalf("status after recovery: %+v", st)
			}
		})
	}
}

// TestPostgresLockTimeoutRetry: a step that waits longer than lock_timeout
// for a lock another connection holds fails with SQLSTATE 55P03, and the
// runner retries it after 1s, 2s, ... until the lock is free. A step outside
// a transaction is retried the same way, running its recovery first.
func TestPostgresLockTimeoutRetry(t *testing.T) {
	for _, c := range []struct {
		name string
		// lockAfter is the step after which the other connection takes
		// the lock; 0 takes it before the run.
		lockAfter int
		// retried is the step the lock times out.
		retried string
	}{
		{"a transactional step", 0, "step 1/5"},
		{"a step outside a transaction", 1, "step 2/5"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			url := testdb.Postgres(t)
			apply(t, newRunner(t, url), plan(t, migrate.Postgres, "01-create"), migrate.All)

			holder := testdb.PostgresConn(t, url)
			released := make(chan struct{})
			lock := func() {
				tx, err := holder.Begin(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				if _, err := tx.Exec(ctx, `LOCK TABLE "order" IN ACCESS EXCLUSIVE MODE`); err != nil {
					t.Error(err)
				}
				time.AfterFunc(700*time.Millisecond, func() {
					_ = tx.Commit(ctx)
					close(released)
				})
			}
			var mu sync.Mutex
			var slept []time.Duration
			log := &testLog{t: t}
			r := &migrate.Runner{
				Driver: openDriver(t, url, 100*time.Millisecond),
				Log:    log,
				Sleep: func(_ context.Context, d time.Duration) error {
					mu.Lock()
					slept = append(slept, d)
					mu.Unlock()
					time.Sleep(200 * time.Millisecond)
					return nil
				},
			}
			if c.lockAfter == 0 {
				lock()
			} else {
				r.AfterStep = func(_ context.Context, step *migrate.Step) error {
					if step.Index == c.lockAfter {
						lock()
					}
					return nil
				}
			}
			result := apply(t, r, plan(t, migrate.Postgres, "02-evolve"), migrate.All)
			<-released
			if !result.Finished {
				t.Fatalf("result %+v", result)
			}
			if len(slept) == 0 || slept[0] != time.Second || (len(slept) > 1 && slept[1] != 2*time.Second) {
				t.Fatalf("the runner waited %v, want 1s, 2s, ...", slept)
			}
			if !log.contains(c.retried) || !log.contains("lock timeout; retrying in 1s") {
				t.Fatalf("the log does not show %s retried", c.retried)
			}
			if c.lockAfter == 1 && !log.contains("running its recovery") {
				t.Fatal("the retried step outside a transaction did not run its recovery")
			}
		})
	}
}

// TestPostgresLockTimeoutGivesUp: a step whose lock never frees fails after
// the last retry with the lock timeout, and the plan stays in progress.
func TestPostgresLockTimeoutGivesUp(t *testing.T) {
	ctx := context.Background()
	url := testdb.Postgres(t)
	apply(t, newRunner(t, url), plan(t, migrate.Postgres, "01-create"), migrate.All)
	holder := testdb.PostgresConn(t, url)
	tx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `LOCK TABLE "order" IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}

	driver := openDriver(t, url, 50*time.Millisecond)
	r := &migrate.Runner{
		Driver: driver,
		Log:    &testLog{t: t},
		Waits:  []time.Duration{time.Millisecond, time.Millisecond},
		Sleep:  func(context.Context, time.Duration) error { return nil },
	}
	evolve := plan(t, migrate.Postgres, "02-evolve")
	_, err = r.Apply(ctx, evolve, migrate.All)
	var stepErr *migrate.StepError
	if !errors.As(err, &stepErr) || stepErr.Index != 1 || !driver.IsLockTimeout(err) {
		t.Fatalf("apply = %v, want step 1 to fail on a lock timeout", err)
	}
	for _, want := range []string{"55P03", "gave up after 2 retries", `ALTER TABLE "order" ADD COLUMN note TEXT`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q does not say %q", err, want)
		}
	}
	if st := status(t, r, "shop"); st.PlanHash != evolve.Hash || len(st.Steps) != 0 {
		t.Fatalf("status %+v", st)
	}
}
