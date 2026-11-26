package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// captureDatabaseEnv names the Postgres the capture test creates its own
// database on.
const captureDatabaseEnv = "SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL"

// buildCaptureFixture builds fixture-capture-db as a build does, with
// names, and returns the output root.
func buildCaptureFixture(t *testing.T, names naming.Naming) string {
	t.Helper()
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-capture-db"))
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if _, err := Run(schema, cfg, Options{
		OutputRoot: out,
		Paths:      testpaths.Local(t),
		Naming:     names,
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	}); err != nil {
		t.Fatalf("run fixture-capture-db: %v", err)
	}
	return out
}

// TestProjectionCaptureIsOffByDefault: without [go.projection_capture]
// the Go types of a schema with projections write no capture file.
func TestProjectionCaptureIsOffByDefault(t *testing.T) {
	out := buildCaptureFixture(t, naming.Default())
	dir := TypesDir(out, "go", "fixture-capture-db")
	if _, err := os.Stat(filepath.Join(dir, "types.go")); err != nil {
		t.Fatalf("the build wrote no Go types: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "projection_capture.go")); !os.IsNotExist(err) {
		t.Errorf("projection_capture.go without the table: %v", err)
	}
}

// TestProjectionCaptureQueriesOnPostgres builds fixture-capture-db with
// [go.projection_capture] set to app.account_id (D168), then runs
// captureTest in its Go types module, after go vet. fixture-capture-db
// has three views: scope.notes reads only app.account_id,
// scope.team_notes also reads the optional app.team_id, and team.notes
// reads app.team_id. The test creates a database of its own on the
// Postgres captureDatabaseEnv names, applies the build's create.sql,
// inserts notes for two accounts, and checks that the capture functions
// list scope.notes alone; that for each account the capture query, run
// after set_config(..., true) in a transaction, returns exactly the rows
// the view serves under that value; and that the uuid columns are the
// view's columns Postgres types uuid. It skips without the database and
// in -short mode.
func TestProjectionCaptureQueriesOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated capture functions in -short mode")
	}
	if os.Getenv(captureDatabaseEnv) == "" {
		t.Skip("set " + captureDatabaseEnv + " to run the generated capture queries against Postgres")
	}
	names := naming.Default()
	names.Go.ProjectionCapture = naming.GoProjectionCapture{
		Queries:     "ScopeCaptureQueries",
		UUIDColumns: "ScopeUUIDColumns",
		Setting:     "app.account_id",
	}
	out := buildCaptureFixture(t, names)
	dir := TypesDir(out, "go", "fixture-capture-db")
	if err := os.MkdirAll(filepath.Join(dir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	createSQL, err := os.ReadFile(filepath.Join(SQLDir(out, "fixture-capture-db"), "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "testdata", "create.sql"), createSQL, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "capture_test.go"), []byte(captureTest), 0o644); err != nil {
		t.Fatal(err)
	}

	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	var output []byte
	for _, args := range [][]string{
		{"mod", "tidy"},
		{"vet", "./..."},
		{"test", "-count=1", "-v", "./..."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = dir
		// Environ sets PWD to Dir, which keeps the module's relative
		// replace paths valid under a symlinked temp dir.
		cmd.Env = append(cmd.Environ(), env...)
		output, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the generated types module: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
	t.Logf("generated types tests:\n%s", output)
	for _, name := range []string{"TestCaptureLists", "TestCaptureQueriesOnPostgres"} {
		if !strings.Contains(string(output), "--- PASS: "+name+" ") {
			t.Errorf("the generated module's test did not pass %s", name)
		}
	}
}

// captureTest runs in the Go types module of fixture-capture-db.
const captureTest = `package types

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const relation = "scope.notes"

// TestCaptureLists: the functions list the view that reads only
// app.account_id, and of its columns the uuid one.
func TestCaptureLists(t *testing.T) {
	if got := slices.Sorted(maps.Keys(ScopeCaptureQueries())); !slices.Equal(got, []string{relation}) {
		t.Errorf("ScopeCaptureQueries lists %v, want [%s]", got, relation)
	}
	columns := ScopeUUIDColumns()
	if got := slices.Sorted(maps.Keys(columns)); !slices.Equal(got, []string{relation}) {
		t.Errorf("ScopeUUIDColumns lists %v, want [%s]", got, relation)
	}
	if got := columns[relation]; !slices.Equal(got, []string{"id"}) {
		t.Errorf("ScopeUUIDColumns()[%q] = %v, want [id]", relation, got)
	}
	// Each call returns a new map.
	ScopeCaptureQueries()[relation] = "changed"
	if ScopeCaptureQueries()[relation] == "changed" {
		t.Error("ScopeCaptureQueries returns a shared map")
	}
}

func TestCaptureQueriesOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL to run the capture queries against Postgres")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer admin.Close(ctx)
	database := fmt.Sprintf("projection_capture_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+database); err != nil {
		t.Fatalf("create database: %v", err)
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+database+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database: %v", err)
		}
	}()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	cfg.Database = database
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect %s: %v", database, err)
	}
	defer conn.Close(ctx)
	createSQL, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}
	if _, err := conn.Exec(ctx, string(createSQL)); err != nil {
		t.Fatalf("apply create.sql: %v", err)
	}

	const (
		accountA = "00000000-0000-4000-8000-00000000000a"
		accountB = "00000000-0000-4000-8000-00000000000b"
		team1    = "00000000-0000-4000-8000-000000000001"
		team2    = "00000000-0000-4000-8000-000000000002"
	)
	notes := []struct{ account, team, title string }{
		{accountA, team1, "a in team 1"},
		{accountA, team2, "a in team 2"},
		{accountB, team1, "b in team 1"},
	}
	for _, note := range notes {
		if _, err := conn.Exec(ctx, "INSERT INTO note (account, team, title) VALUES ($1, $2, $3)", note.account, note.team, note.title); err != nil {
			t.Fatalf("insert %s: %v", note.title, err)
		}
	}

	query := ScopeCaptureQueries()[relation]
	for account, want := range map[string][]string{
		accountA: {"a in team 1", "a in team 2"},
		accountB: {"b in team 1"},
	} {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, "SELECT set_config('app.account_id', $1, true)", account); err != nil {
			t.Fatalf("set app.account_id: %v", err)
		}
		captured := readRows(t, tx, query)
		served := readRows(t, tx, "SELECT * FROM "+relation)
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if !slices.EqualFunc(captured, served, slices.Equal[[]string]) {
			t.Errorf("account %s: the capture query returns\n%v\nand the view serves\n%v", account, captured, served)
		}
		var titles []string
		for _, row := range served {
			titles = append(titles, row[1])
		}
		slices.Sort(titles)
		if !slices.Equal(titles, want) {
			t.Errorf("account %s: the view serves %v, want %v", account, titles, want)
		}
	}

	// The uuid columns are the columns of the view Postgres types uuid.
	rows, err := conn.Query(ctx, "SELECT column_name FROM information_schema.columns WHERE table_schema = 'scope' AND table_name = 'notes' AND data_type = 'uuid' ORDER BY ordinal_position")
	if err != nil {
		t.Fatal(err)
	}
	uuidColumns, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if got := ScopeUUIDColumns()[relation]; !slices.Equal(got, uuidColumns) {
		t.Errorf("ScopeUUIDColumns()[%q] = %v, and Postgres types %v uuid", relation, got, uuidColumns)
	}
}

// readRows runs query in tx and returns each row as its columns' text,
// the rows sorted.
func readRows(t *testing.T, tx pgx.Tx, query string) [][]string {
	t.Helper()
	rows, err := tx.Query(context.Background(), query)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer rows.Close()
	var out [][]string
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			t.Fatal(err)
		}
		row := make([]string, len(values))
		for i, value := range values {
			row[i] = fmt.Sprint(value)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	slices.SortFunc(out, func(a, b []string) int { return slices.Compare(a, b) })
	return out
}
`
