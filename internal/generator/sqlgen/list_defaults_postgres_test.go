package sqlgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestListDefaultsCreateSQLOnPostgres applies the create.sql and drop.sql
// generated for fixture-list-defaults-db to a real Postgres; testdata/pgcheck's
// TestListDefaultsOnPostgres holds the checks. A required list of a temporal
// scalar used to take that scalar's default (CURRENT_TIMESTAMP on a
// TIMESTAMPTZ[] column), which Postgres refuses. Set
// SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL as for
// TestProjectionMigrationsOnPostgres.
func TestListDefaultsCreateSQLOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL to apply fixture-list-defaults-db's create.sql against Postgres")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-list-defaults-db"))
	if err != nil {
		t.Fatalf("load fixture-list-defaults-db: %v", err)
	}
	output, err := Generate(schema, Options{SchemaName: "fixture-list-defaults-db"})
	if err != nil {
		t.Fatal(err)
	}
	sqlDir := t.TempDir()
	if err := WriteDDL(output, sqlDir); err != nil {
		t.Fatal(err)
	}
	runPGCheck(t, "TestListDefaultsOnPostgres",
		"PGCHECK_DATABASE_URL="+dsn,
		"PGCHECK_SQL_DIR="+sqlDir,
	)
}
