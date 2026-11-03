package pgcheck

import (
	"context"
	"testing"
	"time"
)

// TestWorkflowFieldsOnPostgres applies create.sql for
// fixture-workflow-db, whose order's status is a Workflow and whose
// shipment's status and paymentStatus are two (D103). Each owned field is
// a column of its table: the value field NOT NULL with no default, since
// the core sets it on every row it creates, and the field a transition
// stamps nullable. Each table keeps @optimistic's _version, which an
// update bumps, so a write fenced on the version a read saw changes no row
// once another write has moved it. drop.sql removes the tables and the
// triggers. TestWorkflowFieldsCreateSQLOnPostgres in the sqlgen package
// runs it with PGCHECK_DATABASE_URL and PGCHECK_SQL_DIR.
func TestWorkflowFieldsOnPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn := scratchDatabase(t, ctx, "superschematic_workflow_fields")

	execScript(t, ctx, conn, "create.sql", readSQL(t, "create.sql"))
	for _, want := range []struct {
		table, column, typ string
		nullable           bool
	}{
		{"order", "status", "text", false},
		{"order", "status_entered_at", "timestamp with time zone", true},
		{"order", "_version", "bigint", false},
		{"shipment", "status", "text", false},
		{"shipment", "status_entered_at", "timestamp with time zone", true},
		{"shipment", "payment_status", "text", false},
		{"shipment", "payment_status_entered_at", "timestamp with time zone", true},
		{"shipment", "_version", "bigint", false},
	} {
		var typ, nullable string
		var def *string
		if err := conn.QueryRow(ctx, `SELECT data_type, is_nullable, column_default FROM information_schema.columns WHERE table_name = $1 AND column_name = $2`, want.table, want.column).Scan(&typ, &nullable, &def); err != nil {
			t.Fatalf("%s.%s: %v", want.table, want.column, err)
		}
		if typ != want.typ || (nullable == "YES") != want.nullable {
			t.Errorf("%s.%s is %s, nullable %s; want %s, nullable %v", want.table, want.column, typ, nullable, want.typ, want.nullable)
		}
		if want.column != "_version" && def != nil {
			t.Errorf("%s.%s defaults to %s; an owned column takes no default", want.table, want.column, *def)
		}
	}

	var id string
	var version int64
	if err := conn.QueryRow(ctx, `INSERT INTO "order" (title, status, status_entered_at) VALUES ('Desk', 'draft', now()) RETURNING id::text, _version`).Scan(&id, &version); err != nil || version != 1 {
		t.Fatalf("insert an order: version %d, %v", version, err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO "order" (title) VALUES ('Chair')`); err == nil {
		t.Fatal("an order with no status was inserted")
	}
	tag, err := conn.Exec(ctx, `UPDATE "order" SET status = 'paid', status_entered_at = now() WHERE id = $1 AND _version = $2`, id, version)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("fenced update at version %d changed %d rows: %v", version, tag.RowsAffected(), err)
	}
	if err := conn.QueryRow(ctx, `SELECT _version FROM "order" WHERE id = $1`, id).Scan(&version); err != nil || version != 2 {
		t.Fatalf("the update left _version %d: %v", version, err)
	}
	tag, err = conn.Exec(ctx, `UPDATE "order" SET status = 'shipped' WHERE id = $1 AND _version = 1`, id)
	if err != nil || tag.RowsAffected() != 0 {
		t.Fatalf("an update fenced on a moved version changed %d rows: %v", tag.RowsAffected(), err)
	}

	execScript(t, ctx, conn, "drop.sql", readSQL(t, "drop.sql"))
	var tables int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE tablename IN ('order', 'shipment')`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("drop.sql left %d tables: %v", tables, err)
	}
}
