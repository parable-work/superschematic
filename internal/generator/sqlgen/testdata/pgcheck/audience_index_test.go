package pgcheck

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestAudienceIndexOnPostgres applies fixture-access-db's create.sql and
// checks the GIN index over each @audience table's effective audience
// (D156): it exists, is a GIN index, and serves a read's overlap of the
// column with the caller's ids. TestAudienceIndexOnPostgresCreateSQL in
// the sqlgen package runs it with PGCHECK_DATABASE_URL and
// PGCHECK_SQL_DIR.
func TestAudienceIndexOnPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn := scratchDatabase(t, ctx, "superschematic_audience_index")
	execScript(t, ctx, conn, "create.sql", readSQL(t, "create.sql"))

	for _, table := range []string{"document", "folder"} {
		var method string
		err := conn.QueryRow(ctx, `SELECT am.amname FROM pg_class c JOIN pg_am am ON am.oid = c.relam WHERE c.relname = $1`, "idx_"+table+"_effective_audience").Scan(&method)
		if err != nil || method != "gin" {
			t.Fatalf("idx_%s_effective_audience: method %q (%v), want gin", table, method, err)
		}
	}
	mustExec(t, ctx, conn, `SET enable_seqscan = off`)
	rows, err := conn.Query(ctx, `EXPLAIN SELECT id FROM document WHERE effective_audience && ARRAY[$1]::uuid[]`, userOne)
	if err != nil {
		t.Fatal(err)
	}
	var plan []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if text := strings.Join(plan, "\n"); !strings.Contains(text, "idx_document_effective_audience") {
		t.Fatalf("the overlap reads no index:\n%s", text)
	}
}
