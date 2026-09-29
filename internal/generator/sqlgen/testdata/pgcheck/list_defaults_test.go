package pgcheck

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestListDefaultsOnPostgres applies create.sql for the
// fixture-list-defaults-db service, whose tasting table has a required
// Temporal.DateTime, Temporal.Date and Temporal.Time column, a required list
// of each, an auto-generated list, and three JSONB columns (a list of lists,
// a map and a JSON field) that hold them. The script applies. A row that
// names only the JSONB columns takes the current timestamp, date and time
// for the scalars and an empty array for every list. A row that leaves out
// a JSONB column is refused, since those columns have no default. drop.sql
// removes the table. TestListDefaultsCreateSQLOnPostgres in the sqlgen
// package runs it with PGCHECK_DATABASE_URL and PGCHECK_SQL_DIR.
func TestListDefaultsOnPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn := scratchDatabase(t, ctx, "superschematic_list_defaults")

	execScript(t, ctx, conn, "create.sql", readSQL(t, "create.sql"))

	// One statement, so now(), CURRENT_DATE and LOCALTIME read the same
	// transaction start as the column defaults did.
	var scalarsNow bool
	var retastedAt, retastedOn, repouredAt, restockedAt int
	if err := conn.QueryRow(ctx, `
		INSERT INTO tasting (flights, opened_at, bottled_at)
		VALUES ('[]', '{}', '"2026-01-02T03:04:05Z"')
		RETURNING tasted_at = now() AND tasted_on = CURRENT_DATE AND poured_at = LOCALTIME,
			cardinality(retasted_at), cardinality(retasted_on), cardinality(repoured_at), cardinality(restocked_at)`,
	).Scan(&scalarsNow, &retastedAt, &retastedOn, &repouredAt, &restockedAt); err != nil {
		t.Fatalf("insert a tasting that names only its JSONB columns: %v", err)
	}
	if !scalarsNow {
		t.Error("tasted_at, tasted_on and poured_at did not default to the current timestamp, date and time")
	}
	if retastedAt != 0 || retastedOn != 0 || repouredAt != 0 || restockedAt != 0 {
		t.Errorf("list defaults hold %d, %d, %d and %d elements, want empty arrays", retastedAt, retastedOn, repouredAt, restockedAt)
	}

	_, err := conn.Exec(ctx, `INSERT INTO tasting (opened_at, bottled_at) VALUES ('{}', '"2026-01-02T03:04:05Z"')`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23502" || pgErr.ColumnName != "flights" {
		t.Errorf("a tasting without flights: got %v, want a not-null violation on flights", err)
	}

	execScript(t, ctx, conn, "drop.sql", readSQL(t, "drop.sql"))
	var tables int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE tablename = 'tasting'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("drop.sql left %d tables: %v", tables, err)
	}
}
