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

// TestSQLiteForeignKeysOff: a copy-table rebuild of a parent table keeps
// the rows of a child that references it ON DELETE CASCADE when the step
// turns foreign keys off, and loses them when it does not; a rebuild that
// leaves a child row without its parent fails foreign_key_check and rolls
// back.
func TestSQLiteForeignKeysOff(t *testing.T) {
	t.Run("the rebuild keeps the children", func(t *testing.T) {
		url := testdb.NewSQLite(t)
		r := newRunner(t, url)
		apply(t, r, plan(t, migrate.SQLite, "01-create"), migrate.All)
		seed(t, url)
		apply(t, r, plan(t, migrate.SQLite, "02-evolve"), migrate.All)
		if got := count(t, url, `"order"`); got != "3" {
			t.Fatalf("orders after the rebuild: %s", got)
		}
		if got := testdb.Strings(t, url, `PRAGMA foreign_key_check`); len(got) != 0 {
			t.Fatalf("foreign_key_check: %v", got)
		}
	})

	t.Run("with foreign keys on the rebuild deletes them", func(t *testing.T) {
		url := testdb.NewSQLite(t)
		r := newRunner(t, url)
		apply(t, r, plan(t, migrate.SQLite, "01-create"), migrate.All)
		seed(t, url)
		apply(t, r, edited(t, migrate.SQLite, "02-evolve", func(p map[string]any) {
			delete(steps(p)[1].(map[string]any), "foreignKeysOff")
		}), migrate.All)
		if got := count(t, url, `"order"`); got != "0" {
			t.Fatalf("orders after a rebuild with foreign keys on: %s, want the cascade to delete them", got)
		}
	})

	t.Run("a violation rolls the step back", func(t *testing.T) {
		url := testdb.NewSQLite(t)
		r := newRunner(t, url)
		apply(t, r, plan(t, migrate.SQLite, "01-create"), migrate.All)
		seed(t, url)
		violation := plan(t, migrate.SQLite, "fk-violation")
		_, err := r.Apply(context.Background(), violation, migrate.All)
		var stepErr *migrate.StepError
		if !errors.As(err, &stepErr) || stepErr.Index != 1 || !strings.Contains(err.Error(), "foreign_key_check") || !strings.Contains(err.Error(), "order row") {
			t.Fatalf("apply = %v, want step 1 to fail foreign_key_check", err)
		}
		if got := testdb.Strings(t, url, `SELECT name FROM customer ORDER BY id`); strings.Join(got, ",") != "Ada,Grace" {
			t.Fatalf("customers after the rollback: %v", got)
		}
		if got := testdb.Strings(t, url, `SELECT name FROM sqlite_master WHERE name = 'customer__new'`); len(got) != 0 {
			t.Fatalf("the rollback left %v", got)
		}
		if st := status(t, r, "shop"); st.PlanHash != violation.Hash || len(st.Steps) != 0 {
			t.Fatalf("status %+v", st)
		}
		// The runner's connection has foreign keys on again.
		var on int64
		err = r.Driver.Transact(context.Background(), migrate.TxOptions{ReadOnly: true}, func(ctx context.Context, conn migrate.Conn) error {
			return conn.Query(ctx, "PRAGMA foreign_keys", nil, func(scan func(dest ...any) error) error { return scan(&on) })
		})
		if err != nil || on != 1 {
			t.Fatalf("foreign_keys = %d, %v", on, err)
		}
	})
}

// TestSQLiteBusyRetry: a step whose BEGIN IMMEDIATE finds another
// connection's write lock waits busy_timeout, fails with SQLITE_BUSY, and
// the runner retries it after 1s, 2s, ... until the lock is free; when it
// never frees, the step fails after the last retry.
func TestSQLiteBusyRetry(t *testing.T) {
	for _, frees := range []bool{true, false} {
		name := "the lock frees"
		if !frees {
			name = "the lock never frees"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			url := testdb.NewSQLite(t)
			apply(t, newRunner(t, url), plan(t, migrate.SQLite, "01-create"), migrate.All)
			holder, err := testdb.SQLiteDB(t, url).Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = holder.Close() }()

			var mu sync.Mutex
			var slept []time.Duration
			released := make(chan struct{})
			log := &testLog{t: t}
			driver := openDriver(t, url, 50*time.Millisecond)
			r := &migrate.Runner{
				Driver: driver,
				Log:    log,
				Sleep: func(_ context.Context, d time.Duration) error {
					mu.Lock()
					slept = append(slept, d)
					mu.Unlock()
					if frees {
						time.Sleep(150 * time.Millisecond)
					}
					return nil
				},
				AfterStep: func(_ context.Context, step *migrate.Step) error {
					if step.Index != 1 {
						return nil
					}
					if _, err := holder.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
						return err
					}
					if frees {
						time.AfterFunc(500*time.Millisecond, func() {
							_, _ = holder.ExecContext(ctx, "COMMIT")
							close(released)
						})
					}
					return nil
				},
			}
			evolve := plan(t, migrate.SQLite, "02-evolve")
			result, err := r.Apply(ctx, evolve, migrate.All)
			if !frees {
				var stepErr *migrate.StepError
				if !errors.As(err, &stepErr) || stepErr.Index != 2 || !driver.IsLockTimeout(err) || !strings.Contains(err.Error(), "gave up after 5 retries") {
					t.Fatalf("apply = %v, want step 2 to give up on SQLITE_BUSY", err)
				}
				_, _ = holder.ExecContext(ctx, "ROLLBACK")
				if st := status(t, r, "shop"); st.PlanHash != evolve.Hash || len(st.Steps) != 1 {
					t.Fatalf("status %+v", st)
				}
				return
			}
			<-released
			if err != nil || !result.Finished {
				t.Fatalf("apply = %+v, %v", result, err)
			}
			if len(slept) == 0 || slept[0] != time.Second {
				t.Fatalf("the runner waited %v, want 1s, ...", slept)
			}
			if !log.contains("step 2/4 expand table/customer: lock timeout; retrying in 1s") {
				t.Fatal("the log does not show step 2 retried")
			}
		})
	}
}
