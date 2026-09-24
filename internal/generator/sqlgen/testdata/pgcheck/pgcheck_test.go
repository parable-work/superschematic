// Package pgcheck applies the SQL the sqlgen package generated for the
// fixture-projection service to a real Postgres and checks what the view
// does: it fails closed without its settings, serves only the rows its
// rules admit, keeps one row per slot, belongs to the view owner, is a
// security barrier, hides the base tables from a reader, and has the
// columns its Arrow schema lists. TestProjectionMigrationsOnPostgres in the
// sqlgen package runs it with:
//
//	PGCHECK_DATABASE_URL  a Postgres URL whose role may create databases and roles
//	PGCHECK_SQL_DIR       the directory holding create.sql, drop.sql and the migrations
//	PGCHECK_UP, PGCHECK_DOWN  the migration file names
//	PGCHECK_ARROW         the Arrow schema file
//	PGCHECK_VIEW_OWNER    the role the up migration creates the view as
package pgcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	accountA = "0000000a-0000-4000-8000-000000000000"
	accountB = "0000000b-0000-4000-8000-000000000000"
	userOne  = "00000001-0000-4000-8000-000000000000"
	userTwo  = "00000002-0000-4000-8000-000000000000"
	stableA  = "0000000a-0000-4000-8000-00000000c001"
	betaA    = "0000000a-0000-4000-8000-00000000c002"
	stableB  = "0000000b-0000-4000-8000-00000000c001"
	release1 = "0000000a-0000-4000-8000-0000000000e1"
	slotOne  = "00000000-0000-4000-8000-0000000005a1"
	slotTwo  = "00000000-0000-4000-8000-0000000005a2"
	slotArch = "00000000-0000-4000-8000-0000000005a3"
	slotHide = "00000000-0000-4000-8000-0000000005a4"
	slotB    = "00000000-0000-4000-8000-0000000005b1"
	slotUser = "00000000-0000-4000-8000-0000000005a6"
)

func env(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Skipf("%s is not set; run through TestProjectionMigrationsOnPostgres", name)
	}
	return v
}

func readSQL(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(env(t, "PGCHECK_SQL_DIR"), name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func execScript(t *testing.T, ctx context.Context, conn *pgx.Conn, name, sql string) {
	t.Helper()
	if _, err := conn.PgConn().Exec(ctx, sql).ReadAll(); err != nil {
		t.Fatalf("apply %s: %v", name, err)
	}
}

func mustExec(t *testing.T, ctx context.Context, conn *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// row is one served row, reduced to what the checks compare.
type row struct {
	slot, scope, channelHandle string
	branchName                 *string
}

// query reads the view as the reader role with the given settings, inside
// a transaction that is rolled back.
func query(ctx context.Context, conn *pgx.Conn, reader string, settings map[string]string) ([]row, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+pgx.Identifier{reader}.Sanitize()); err != nil {
		return nil, err
	}
	for name, value := range settings {
		if _, err := tx.Exec(ctx, "SELECT set_config($1, $2, true)", name, value); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT slot_key::text, scope, channel_handle::text, branch_name FROM app.preferences ORDER BY slot_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.slot, &r.scope, &r.channelHandle, &r.branchName); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func TestProjectionOnPostgres(t *testing.T) {
	adminURL := env(t, "PGCHECK_DATABASE_URL")
	owner := env(t, "PGCHECK_VIEW_OWNER")
	reader := owner + "_reader"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// Cleanups run last-registered first: the database and roles are
	// dropped before the admin connection closes.
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	database := fmt.Sprintf("superschematic_projection_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanup := context.Background()
		for _, stmt := range []string{
			"DROP DATABASE IF EXISTS " + database + " WITH (FORCE)",
			"DROP ROLE IF EXISTS " + pgx.Identifier{reader}.Sanitize(),
			"DROP ROLE IF EXISTS " + pgx.Identifier{owner}.Sanitize(),
		} {
			if _, err := admin.Exec(cleanup, stmt); err != nil {
				t.Errorf("cleanup %s: %v", stmt, err)
			}
		}
	})
	mustExec(t, ctx, admin, "CREATE DATABASE "+database)
	for _, role := range []string{owner, reader} {
		mustExec(t, ctx, admin, "CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" NOLOGIN")
	}

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

	// create.sql builds the tables and the view as its runner.
	execScript(t, ctx, conn, "create.sql", readSQL(t, "create.sql"))
	var views int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_views WHERE schemaname = 'app' AND viewname = 'preferences'`).Scan(&views); err != nil || views != 1 {
		t.Fatalf("create.sql left %d views: %v", views, err)
	}

	// The down migration drops the view as the runner.
	execScript(t, ctx, conn, "down migration", readSQL(t, env(t, "PGCHECK_DOWN")))
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_views WHERE schemaname = 'app' AND viewname = 'preferences'`).Scan(&views); err != nil || views != 0 {
		t.Fatalf("the down migration left %d views: %v", views, err)
	}

	// What a deployment's own bootstrap migration grants: the owner may
	// create in the pool schema and read the base tables; the reader sees
	// every view the owner creates there and nothing else.
	for _, stmt := range []string{
		"GRANT USAGE, CREATE ON SCHEMA app TO " + owner,
		"GRANT SELECT ON channel, preference TO " + owner,
		"GRANT USAGE ON SCHEMA app TO " + reader,
		"ALTER DEFAULT PRIVILEGES FOR ROLE " + owner + " IN SCHEMA app GRANT SELECT ON TABLES TO " + reader,
	} {
		mustExec(t, ctx, conn, stmt)
	}

	// The up migration creates the view as the owner, twice: it is
	// re-runnable because it drops the view it is about to create.
	up := readSQL(t, env(t, "PGCHECK_UP"))
	execScript(t, ctx, conn, "up migration", up)
	execScript(t, ctx, conn, "up migration again", up)
	var viewOwner, currentUser string
	var options []string
	if err := conn.QueryRow(ctx, `
		SELECT pg_get_userbyid(c.relowner), coalesce(c.reloptions, '{}'), current_user
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'app' AND c.relname = 'preferences'`).Scan(&viewOwner, &options, &currentUser); err != nil {
		t.Fatal(err)
	}
	if viewOwner != owner {
		t.Errorf("view owner = %s, want %s", viewOwner, owner)
	}
	if currentUser == owner {
		t.Error("the up migration left the session in the owner role; RESET ROLE did not run")
	}
	if !slices.Contains(options, "security_barrier=true") {
		t.Errorf("view options = %v, want security_barrier=true", options)
	}

	// The view's columns are the Arrow schema's fields, in order.
	var arrow struct {
		Fields []struct {
			Name string `json:"name"`
		} `json:"fields"`
	}
	raw, err := os.ReadFile(env(t, "PGCHECK_ARROW"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &arrow); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, f := range arrow.Fields {
		want = append(want, f.Name)
	}
	colRows, err := conn.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema = 'app' AND table_name = 'preferences' ORDER BY ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(colRows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("view columns = %v, Arrow fields = %v", got, want)
	}

	seed(t, ctx, conn)

	// Without its required settings the view raises rather than serving
	// every account's rows. Which unset setting Postgres reports first is
	// the planner's choice.
	if _, err := query(ctx, conn, reader, nil); err == nil || !strings.Contains(err.Error(), `unrecognized configuration parameter "app.`) {
		t.Fatalf("an unset required setting must make the view raise, got %v", err)
	}
	if _, err := query(ctx, conn, reader, map[string]string{"app.account_id": "", "app.channel_id": stableA, "app.user_id": userOne}); err == nil {
		t.Fatal("an empty required setting must fail the uuid cast")
	}

	scopeA := map[string]string{"app.account_id": accountA, "app.channel_id": stableA, "app.user_id": userOne, "app.branch_id": betaA}
	for _, tc := range []struct {
		name     string
		settings map[string]string
		want     []string
	}{
		// slotOne: the user row wins over team and app (rank), slotTwo's app
		// row needs the release setting, the archived and hidden rows and
		// account B's row never appear, and userTwo's row is not userOne's.
		{"user one on the branch", scopeA, []string{slotOne + ":user:stable:-"}},
		{"user two sees the team row and their own", with(scopeA, "app.user_id", userTwo), []string{slotOne + ":team:stable:-", slotUser + ":user:stable:-"}},
		{"the release setting admits the released app row", with(scopeA, "app.commit_id", release1), []string{slotOne + ":user:stable:-", slotTwo + ":app:stable:-"}},
		{"an empty optional setting matches no row", with(with(scopeA, "app.user_id", userTwo), "app.branch_id", ""), []string{slotOne + ":team:stable:-", slotUser + ":user:stable:-"}},
		{"no branch leaves the branch row out, the team row wins", map[string]string{"app.account_id": accountA, "app.channel_id": stableA, "app.user_id": accountA}, []string{slotOne + ":team:stable:-"}},
		{"account B sees only its row", map[string]string{"app.account_id": accountB, "app.channel_id": stableB, "app.user_id": userOne}, []string{slotB + ":team:stable-b:-"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := query(ctx, conn, reader, tc.settings)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, r := range rows {
				branch := "-"
				if r.branchName != nil {
					branch = *r.branchName
				}
				got = append(got, fmt.Sprintf("%s:%s:%s:%s", r.slot, r.scope, r.channelHandle, branch))
			}
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("rows = %v, want %v", got, tc.want)
			}
		})
	}

	// The winning app row carries its branch's name through the left join.
	rows, err := query(ctx, conn, reader, map[string]string{"app.account_id": accountA, "app.channel_id": stableA, "app.user_id": accountB, "app.branch_id": betaA})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].scope != "team" {
		t.Fatalf("rows = %+v, want slotOne's team row", rows)
	}
	mustExec(t, ctx, conn, `UPDATE preference SET archived_at = now() WHERE slot_key = $1 AND scope <> 'app'`, slotOne)
	rows, err = query(ctx, conn, reader, map[string]string{"app.account_id": accountA, "app.channel_id": stableA, "app.user_id": accountB, "app.branch_id": betaA})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].scope != "app" || rows[0].branchName == nil || *rows[0].branchName != "beta" {
		t.Fatalf("rows = %+v, want slotOne's app row on the beta branch", rows)
	}

	// The reader reaches the rows only through the view.
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mustTx := func(sql string) error {
		_, err := tx.Exec(ctx, sql)
		return err
	}
	if err := mustTx("SET LOCAL ROLE " + reader); err != nil {
		t.Fatal(err)
	}
	if err := mustTx("SELECT * FROM preference"); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("the reader read the base table: %v", err)
	}
	_ = tx.Rollback(ctx)

	execScript(t, ctx, conn, "drop.sql", readSQL(t, "drop.sql"))
	var tables int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE tablename IN ('channel', 'preference')`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("drop.sql left %d tables: %v", tables, err)
	}
}

func with(settings map[string]string, name, value string) map[string]string {
	out := make(map[string]string, len(settings)+1)
	for k, v := range settings {
		out[k] = v
	}
	out[name] = value
	return out
}

// seed writes two accounts' channels and preferences.
func seed(t *testing.T, ctx context.Context, conn *pgx.Conn) {
	t.Helper()
	for _, c := range [][4]string{
		{stableA, accountA, "stable", "stable"},
		{betaA, accountA, "beta", "beta"},
		{stableB, accountB, "stable", "stable-b"},
	} {
		mustExec(t, ctx, conn, `INSERT INTO channel (id, account, "name", handle) VALUES ($1, $2, $3, $4)`, c[0], c[1], c[2], c[3])
	}
	type pref struct {
		account, channel, slot, scope string
		branch, commit, user          *string
		archived, hidden              bool
	}
	ptr := func(s string) *string { return &s }
	for _, p := range []pref{
		{account: accountA, channel: stableA, slot: slotOne, scope: "app", branch: ptr(betaA)},
		{account: accountA, channel: stableA, slot: slotOne, scope: "team"},
		{account: accountA, channel: stableA, slot: slotOne, scope: "user", user: ptr(userOne)},
		{account: accountA, channel: stableA, slot: slotTwo, scope: "app", commit: ptr(release1)},
		{account: accountA, channel: stableA, slot: slotArch, scope: "team", archived: true},
		{account: accountA, channel: stableA, slot: slotHide, scope: "team", hidden: true},
		{account: accountA, channel: stableA, slot: slotUser, scope: "user", user: ptr(userTwo)},
		{account: accountB, channel: stableB, slot: slotB, scope: "team"},
	} {
		var archivedAt *time.Time
		if p.archived {
			now := time.Now()
			archivedAt = &now
		}
		mustExec(t, ctx, conn, `
			INSERT INTO preference (account, channel_id, branch_id, "commit", scope, user_id, slot_key, handle, value, revision, archived_at, hidden)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 'theme', '"dark"', 1, $8, $9)`,
			p.account, p.channel, p.branch, p.commit, p.scope, p.user, p.slot, archivedAt, p.hidden)
	}
}
