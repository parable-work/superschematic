package pgcheck

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestNullsNotDistinctOnPostgres applies create.sql for a table of invoice
// lines whose unique index over (invoice_id, sku) makes nulls collide
// (D125), and checks what Postgres makes of it: a second line of one
// invoice with no sku is a unique violation of the index, and lines with
// different skus, or of another invoice, are not.
// TestNullsNotDistinctOnPostgresCreateSQL in the sqlgen package runs it
// with PGCHECK_DATABASE_URL and PGCHECK_SQL_DIR.
func TestNullsNotDistinctOnPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn := scratchDatabase(t, ctx, "superschematic_nulls")
	execScript(t, ctx, conn, "create.sql", readSQL(t, "create.sql"))

	mustExec(t, ctx, conn, `INSERT INTO invoice_line (id, invoice_id, sku) VALUES ('1', 'inv-1', NULL)`)
	mustExec(t, ctx, conn, `INSERT INTO invoice_line (id, invoice_id, sku) VALUES ('2', 'inv-1', 'apple')`)
	mustExec(t, ctx, conn, `INSERT INTO invoice_line (id, invoice_id, sku) VALUES ('3', 'inv-2', NULL)`)

	_, err := conn.Exec(ctx, `INSERT INTO invoice_line (id, invoice_id, sku) VALUES ('4', 'inv-1', NULL)`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != "uq_invoice_line_invoice_sku" {
		t.Fatalf("a second line of inv-1 with no sku: got %v, want a unique violation of uq_invoice_line_invoice_sku", err)
	}
	var nullsNotDistinct bool
	if err := conn.QueryRow(ctx, `SELECT indnullsnotdistinct FROM pg_index WHERE indexrelid = 'uq_invoice_line_invoice_sku'::regclass`).Scan(&nullsNotDistinct); err != nil || !nullsNotDistinct {
		t.Fatalf("pg_index.indnullsnotdistinct = %v (%v), want true", nullsNotDistinct, err)
	}
}
