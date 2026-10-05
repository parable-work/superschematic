package sqlgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/pgtest"
)

// runPGCheck copies testdata/pgcheck to a temporary module and runs its
// test named test there with env added to the environment. It fails t when
// that test fails or does not run.
func runPGCheck(t *testing.T, test string, env ...string) {
	t.Helper()
	module := t.TempDir()
	if err := os.CopyFS(module, os.DirFS(filepath.Join("testdata", "pgcheck"))); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "-count=1", "-v", "-run", "^"+test+"$", "./...")
	cmd.Dir = module
	cmd.Env = append(append(os.Environ(), "GOFLAGS=-mod=readonly"), env...)
	out, err := cmd.CombinedOutput()
	t.Logf("pgcheck:\n%s", out)
	if err != nil {
		t.Fatalf("pgcheck failed: %v", err)
	}
	if !strings.Contains(string(out), "--- PASS: "+test) {
		t.Fatalf("pgcheck did not run %s", test)
	}
}

// TestUserTableCreateSQLOnPostgres applies the create.sql and drop.sql
// generated for userTableSchema to a real Postgres; testdata/pgcheck's
// TestUserTableOnPostgres holds the checks. Set
// SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL as for
// TestProjectionMigrationsOnPostgres.
func TestUserTableCreateSQLOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL to apply a User table's create.sql against Postgres")
	}
	output, err := Generate(userTableSchema(), Options{SchemaName: "users"})
	if err != nil {
		t.Fatal(err)
	}
	sqlDir := t.TempDir()
	if err := WriteDDL(output, sqlDir); err != nil {
		t.Fatal(err)
	}
	runPGCheck(t, "TestUserTableOnPostgres",
		"PGCHECK_DATABASE_URL="+dsn,
		"PGCHECK_SQL_DIR="+sqlDir,
	)
}

// TestPruneHistoryDropSQLOnPostgres applies the create.sql and drop.sql
// generated for fixture-version-graph-db, whose step and ingredient tables
// declare retentionDays, to a real Postgres; testdata/pgcheck's
// TestPruneHistoryDropOnPostgres holds the checks. drop.sql used to drop
// each prune function by a one-INTEGER signature that matched nothing, so
// the functions outlived it. Set SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL as
// for TestProjectionMigrationsOnPostgres.
func TestPruneHistoryDropSQLOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL to apply fixture-version-graph-db's create.sql and drop.sql against Postgres")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-version-graph-db"))
	if err != nil {
		t.Fatalf("load fixture-version-graph-db: %v", err)
	}
	output, err := Generate(schema, Options{SchemaName: "fixture-version-graph-db"})
	if err != nil {
		t.Fatal(err)
	}
	sqlDir := t.TempDir()
	if err := WriteDDL(output, sqlDir); err != nil {
		t.Fatal(err)
	}
	runPGCheck(t, "TestPruneHistoryDropOnPostgres",
		"PGCHECK_DATABASE_URL="+dsn,
		"PGCHECK_SQL_DIR="+sqlDir,
	)
}

// TestConcurrentCreateSQLOnPostgres applies fixture-db's create.sql, whose
// extensions are pgcrypto, citext and pg_trgm, from several connections at
// once on one database, as pgtest.CreateSQL prepares it for the tests that
// share a database and as it ships; testdata/pgcheck's
// TestConcurrentCreateOnPostgres holds the checks. Set
// SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL as for
// TestProjectionMigrationsOnPostgres.
func TestConcurrentCreateSQLOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL to apply fixture-db's create.sql concurrently against Postgres")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	output, err := Generate(schema, Options{SchemaName: "fixture-db"})
	if err != nil {
		t.Fatal(err)
	}
	sqlDir := t.TempDir()
	if err := WriteDDL(output, sqlDir); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(sqlDir, "create.sql"), filepath.Join(sqlDir, "shared_create.sql"))
	runPGCheck(t, "TestConcurrentCreateOnPostgres",
		"PGCHECK_DATABASE_URL="+dsn,
		"PGCHECK_SQL_DIR="+sqlDir,
	)
}
