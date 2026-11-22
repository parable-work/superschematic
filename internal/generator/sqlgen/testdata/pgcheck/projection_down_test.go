package pgcheck

import (
	"context"
	"slices"
	"testing"
	"time"
)

// TestProjectionDownRestoresThePreviousViewOnPostgres applies create.sql for
// fixture-projection's tables, then the earlier stamp's up migration, then
// the later stamp's, which drops the handle column, and then the later
// stamp's down migration (D128). After the later up the view has no handle;
// after the down it is the earlier view again, handle included.
// TestReplacementDownOnPostgres in the sqlgen package runs it with
// PGCHECK_DATABASE_URL, PGCHECK_SQL_DIR, PGCHECK_EARLIER_UP, PGCHECK_UP and
// PGCHECK_DOWN.
func TestProjectionDownRestoresThePreviousViewOnPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn := scratchDatabase(t, ctx, "superschematic_projection_down")
	execScript(t, ctx, conn, "create.sql", readSQL(t, "create.sql"))

	columns := func() []string {
		t.Helper()
		rows, err := conn.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema = 'app' AND table_name = 'preferences' ORDER BY ordinal_position`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			out = append(out, name)
		}
		return out
	}

	earlierUp := env(t, "PGCHECK_EARLIER_UP")
	execScript(t, ctx, conn, earlierUp, readSQL(t, earlierUp))
	earlier := columns()
	if !slices.Contains(earlier, "handle") {
		t.Fatalf("the earlier view has columns %v, want handle among them", earlier)
	}

	up := env(t, "PGCHECK_UP")
	execScript(t, ctx, conn, up, readSQL(t, up))
	if later := columns(); slices.Contains(later, "handle") {
		t.Fatalf("the later view still has handle: %v", later)
	}

	down := env(t, "PGCHECK_DOWN")
	execScript(t, ctx, conn, down, readSQL(t, down))
	if back := columns(); !slices.Equal(back, earlier) {
		t.Fatalf("after the down the view has columns %v, want the earlier view's %v", back, earlier)
	}
}
