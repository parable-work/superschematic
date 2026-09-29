package pgcheck

import (
	"context"
	"testing"
	"time"
)

// TestUserTableOnPostgres applies create.sql for a schema whose User table
// has an id, email, name and two audit timestamps, and none of the other
// columns of the system user row create.sql used to insert. The script
// applies, leaves the table empty, the table takes a row that names only
// email and name and records its history, and drop.sql removes both
// tables. TestUserTableCreateSQLOnPostgres in the sqlgen package runs it
// with PGCHECK_DATABASE_URL and PGCHECK_SQL_DIR.
func TestUserTableOnPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn := scratchDatabase(t, ctx, "superschematic_user_table")

	execScript(t, ctx, conn, "create.sql", readSQL(t, "create.sql"))
	var users int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM "user"`).Scan(&users); err != nil || users != 0 {
		t.Fatalf("create.sql left %d rows in user: %v", users, err)
	}

	var id string
	if err := conn.QueryRow(ctx, `INSERT INTO "user" (email, "name") VALUES ('ada@example.com', 'Ada') RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("insert a user: %v", err)
	}
	var history int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM user_history WHERE id = $1 AND operation = 'INSERT'`, id).Scan(&history); err != nil || history != 1 {
		t.Fatalf("user_history has %d INSERT rows for the new user: %v", history, err)
	}

	execScript(t, ctx, conn, "drop.sql", readSQL(t, "drop.sql"))
	var tables int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE tablename IN ('user', 'user_history')`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("drop.sql left %d tables: %v", tables, err)
	}
}
