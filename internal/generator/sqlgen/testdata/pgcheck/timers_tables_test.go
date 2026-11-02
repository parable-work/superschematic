package pgcheck

import (
	"context"
	"testing"
	"time"
)

// TestTimersTablesOnPostgres applies fixture-timers-db's create.sql (D103):
// a sub-table of the row, one row per deadline name, deleted with the row;
// and a deadline queue that holds one live message per row, use and
// deadline, beside any number of done ones, which the adapters' arm
// deletes before it inserts.
func TestTimersTablesOnPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn := scratchDatabase(t, ctx, "superschematic_timers_tables")

	execScript(t, ctx, conn, "create.sql", readSQL(t, "create.sql"))
	var ticket string
	if err := conn.QueryRow(ctx, `INSERT INTO ticket (title, status) VALUES ('Printer jams', 'open') RETURNING id::text`).Scan(&ticket); err != nil {
		t.Fatalf("insert a ticket: %v", err)
	}
	insert := `INSERT INTO ticket_timers_deadlines (ticket_id, "name", base_at, armed_from, due_at, fired) VALUES ($1, 'respond', now(), '1', now(), false) ON CONFLICT (ticket_id, "name") DO NOTHING`
	for i, want := range []int64{1, 0} {
		tag, err := conn.Exec(ctx, insert, ticket)
		if err != nil || tag.RowsAffected() != want {
			t.Fatalf("insert %d of one deadline's row changed %d rows, want %d: %v", i+1, tag.RowsAffected(), want, err)
		}
	}
	arm := `INSERT INTO ticket_deadline_queue (row_key, use_id, "name", at, payload, due_at) VALUES ($1, 'Timers', 'respond', 1791536401500, NULL, to_timestamp(1791536401500 / 1000.0))`
	if _, err := conn.Exec(ctx, arm, ticket); err != nil {
		t.Fatalf("arm a deadline: %v", err)
	}
	if _, err := conn.Exec(ctx, arm, ticket); err == nil {
		t.Fatal("a second live message of one deadline was inserted")
	}
	if _, err := conn.Exec(ctx, `UPDATE ticket_deadline_queue SET state = 'done' WHERE row_key = $1`, ticket); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, arm, ticket); err != nil {
		t.Fatalf("arm a deadline again beside its done message: %v", err)
	}
	if _, err := conn.Exec(ctx, `DELETE FROM ticket WHERE id = $1`, ticket); err != nil {
		t.Fatalf("delete the ticket: %v", err)
	}
	var rows int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM ticket_timers_deadlines WHERE ticket_id = $1`, ticket).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("deleting the ticket left %d of its deadlines' rows: %v", rows, err)
	}

	execScript(t, ctx, conn, "drop.sql", readSQL(t, "drop.sql"))
	var tables int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE tablename IN ('ticket', 'ticket_timers_deadlines', 'ticket_deadline_queue', 'invoice', 'invoice_timers_deadlines', 'invoice_deadline_queue')`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("drop.sql left %d tables: %v", tables, err)
	}
}
