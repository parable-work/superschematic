package pgcheck

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestUserTableOnPostgres applies create.sql for a schema whose User table
// has an id, email, name and two audit timestamps, and none of the other
// columns of the system user row create.sql used to insert. The script
// applies, leaves the table empty, the table takes a row that names only
// email and name and records its history, and drop.sql removes both
// tables. TestUserTableCreateSQLOnPostgres in the sqlgen package runs it
// with PGCHECK_DATABASE_URL and PGCHECK_SQL_DIR.
func TestUserTableOnPostgres(t *testing.T) {
	adminURL := env(t, "PGCHECK_DATABASE_URL")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	database := fmt.Sprintf("superschematic_user_table_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+database+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database: %v", err)
		}
	})
	mustExec(t, ctx, admin, "CREATE DATABASE "+database)

	dbURL, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	dbURL.Path = "/" + database
	conn, err := pgx.Connect(ctx, dbURL.String())
	if err != nil {
		t.Fatalf("connect %s: %v", database, err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

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
