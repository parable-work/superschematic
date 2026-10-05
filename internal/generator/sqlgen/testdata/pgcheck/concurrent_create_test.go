package pgcheck

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestConcurrentCreateOnPostgres applies create.sql from several
// connections at once, each in a schema of its own on one database, as the
// Postgres-backed tests in parallel packages share theirs. Each round starts
// with none of its extensions in the database, so any connection may be
// the one to create them. Prepared by pgtest.CreateSQL (shared_create.sql),
// every apply succeeds; create.sql as it ships, applied the same way, is
// only logged, since whether it collides is a race.
// TestConcurrentCreateSQLOnPostgres in the sqlgen package runs it with
// PGCHECK_DATABASE_URL and PGCHECK_SQL_DIR.
func TestConcurrentCreateOnPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn := scratchDatabase(t, ctx, "superschematic_concurrent_create")
	shared, shipped := readSQL(t, "shared_create.sql"), readSQL(t, "create.sql")

	const rounds, sessions = 10, 8
	collided := 0
	for round := range rounds {
		if errs := applyAtOnce(t, ctx, conn, shared, sessions); len(errs) > 0 {
			t.Fatalf("round %d: %d of %d applies failed, the first: %v", round, len(errs), sessions, errs[0])
		}
		if errs := applyAtOnce(t, ctx, conn, shipped, sessions); len(errs) > 0 {
			if collided == 0 {
				t.Logf("create.sql as it ships, round %d: %v", round, errs[0])
			}
			collided++
		}
	}
	t.Logf("create.sql as it ships collided in %d of %d rounds", collided, rounds)
}

// applyAtOnce applies sql from n connections to conn's database, each with a
// schema of its own first on its search path, released together, and
// returns the errors. It then drops the schemas and every extension but
// plpgsql, so the next call starts without them.
func applyAtOnce(t *testing.T, ctx context.Context, conn *pgx.Conn, sql string, n int) []error {
	t.Helper()
	conns := make([]*pgx.Conn, n)
	for i := range conns {
		schema := fmt.Sprintf("session_%d", i)
		mustExec(t, ctx, conn, "CREATE SCHEMA "+schema)
		config := conn.Config().Copy()
		config.RuntimeParams["search_path"] = schema + ",public"
		c, err := pgx.ConnectConfig(ctx, config)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		conns[i] = c
	}
	start := make(chan struct{})
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i, c := range conns {
		wg.Go(func() {
			<-start
			_, errs[i] = c.PgConn().Exec(ctx, sql).ReadAll()
		})
	}
	close(start)
	wg.Wait()

	for i, c := range conns {
		_ = c.Close(ctx)
		mustExec(t, ctx, conn, fmt.Sprintf("DROP SCHEMA session_%d CASCADE", i))
	}
	rows, _ := conn.Query(ctx, "SELECT extname FROM pg_extension WHERE extname <> 'plpgsql'")
	extensions, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("list extensions: %v", err)
	}
	for _, extension := range extensions {
		mustExec(t, ctx, conn, "DROP EXTENSION "+extension+" CASCADE")
	}
	return slices.DeleteFunc(errs, func(err error) bool { return err == nil })
}
