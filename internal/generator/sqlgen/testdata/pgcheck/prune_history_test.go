package pgcheck

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestPruneHistoryDropOnPostgres applies create.sql for the
// fixture-version-graph-db service, whose step and ingredient tables declare
// retentionDays, and checks that it creates their two-argument prune
// functions and that drop.sql leaves no function named %_prune_history
// behind. Postgres matches DROP FUNCTION by argument types, so a drop that
// names the wrong ones only notices that nothing matched.
// TestPruneHistoryDropSQLOnPostgres in the sqlgen package runs it with
// PGCHECK_DATABASE_URL and PGCHECK_SQL_DIR.
func TestPruneHistoryDropOnPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn := scratchDatabase(t, ctx, "superschematic_prune_history")

	execScript(t, ctx, conn, "create.sql", readSQL(t, "create.sql"))
	want := []string{"ingredient_prune_history(integer,integer)", "step_prune_history(integer,integer)"}
	if got := pruneFunctions(t, ctx, conn); !slices.Equal(got, want) {
		t.Fatalf("create.sql left prune functions %v, want %v", got, want)
	}

	execScript(t, ctx, conn, "drop.sql", readSQL(t, "drop.sql"))
	if got := pruneFunctions(t, ctx, conn); len(got) != 0 {
		t.Fatalf("drop.sql left prune functions %v", got)
	}
}

// pruneFunctions returns the signature of every function named
// %_prune_history, in order.
func pruneFunctions(t *testing.T, ctx context.Context, conn *pgx.Conn) []string {
	t.Helper()
	rows, err := conn.Query(ctx, `SELECT oid::regprocedure::text FROM pg_proc WHERE proname LIKE '%\_prune\_history' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return got
}
