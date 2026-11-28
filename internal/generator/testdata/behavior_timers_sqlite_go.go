// fixture-timers-api's behavior worker TicketTimers over fixture-timers-db's
// SQLite file in Go (#717), written into the generated API module by
// TestTimersGoWorkerOnSQLite: the queues the API's TicketTimersQueues
// builds over the ORM's SQLiteDatabase claim on the file, run a due
// deadline through the behaviors' core in the repository's transaction on
// SQLite, and drain.
package fixturetimersapi_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/runtime/http/go/worker"
	"go.uber.org/zap"
	_ "modernc.org/sqlite"

	api "example.com/schemas/api/fixture-timers-api"
	orm "example.com/schemas/orm/fixture-timers-db"
)

func TestTimersOnSQLite(t *testing.T) {
	ctx := context.Background()
	raw, err := sql.Open("sqlite", orm.SQLiteDSN(filepath.Join(t.TempDir(), "timers.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	ddl, err := os.ReadFile("testdata/sqlite.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, string(ddl)); err != nil {
		t.Fatalf("apply the SQLite DDL: %v", err)
	}
	db, err := orm.OpenSQLite(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	row, err := db.GetTicketRepository().CreateOne(ctx, &orm.TicketCreate{Title: "Printer"})
	if err != nil {
		t.Fatalf("create a ticket: %v", err)
	}
	column := row.Id.ToUUID().String()
	status := func(t *testing.T) string {
		t.Helper()
		var s string
		if err := raw.QueryRowContext(ctx, "SELECT status FROM ticket WHERE id = ?1", column).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	live := func(t *testing.T) int {
		t.Helper()
		var n int
		if err := raw.QueryRowContext(ctx, "SELECT count(*) FROM ticket_deadline_queue WHERE row_key = ?1 AND state IN ('ready', 'claimed')", column).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("a due deadline the worker runs moves the status and records the outcome", func(t *testing.T) {
		queues := api.TicketTimersQueues(db, zap.NewNop())
		if len(queues) != 2 || queues[0].Name != "TicketDeadline" || queues[1].Name != "TicketChange" {
			t.Fatalf("the worker's queues are %d, want TicketDeadline and TicketChange", len(queues))
		}
		for _, q := range queues {
			if q.Slot != nil || q.NextDue != nil {
				t.Errorf("queue %s on SQLite takes a slot or reads a next due time", q.Name)
			}
		}
		if live(t) != 1 {
			t.Fatalf("the ticket's live deadline messages are %d, want respond's", live(t))
		}
		running, stop := context.WithCancel(ctx)
		stopped := make(chan error, 1)
		go func() {
			stopped <- worker.RunAll(running, queues, worker.Options{Concurrency: 2, Idle: 100 * time.Millisecond})
		}()
		for deadline := time.Now().Add(10 * time.Second); status(t) != "escalated" && time.Now().Before(deadline); {
			time.Sleep(100 * time.Millisecond)
		}
		stop()
		if err := <-stopped; err != nil {
			t.Fatalf("the worker: %v", err)
		}
		if got := status(t); got != "escalated" {
			t.Fatalf("status = %s, want escalated within ten seconds", got)
		}
		var fired bool
		var outcome string
		if err := raw.QueryRowContext(ctx, "SELECT fired, outcome FROM ticket_timers_deadlines WHERE ticket_id = ?1 AND name = 'respond'", column).Scan(&fired, &outcome); err != nil {
			t.Fatal(err)
		}
		if !fired || outcome != "moved" {
			t.Fatalf("respond fired %v with %q, want fired, moved", fired, outcome)
		}
		if n := live(t); n != 0 {
			t.Fatalf("the ticket's live deadline messages are %d, want none: the run spent its message", n)
		}
	})

	t.Run("in drain mode the worker stops once its queues are idle", func(t *testing.T) {
		done := make(chan error, 1)
		go func() {
			done <- worker.RunAll(ctx, api.TicketTimersQueues(db, zap.NewNop()), worker.Options{Concurrency: 2, Idle: 50 * time.Millisecond, Drain: &worker.Drain{Idle: 300 * time.Millisecond}})
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("the drain: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the drain did not stop within ten seconds of its queues going idle")
		}
	})
}
