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

// TestDDLCreateSQLOnPostgres applies the create.sql and drop.sql generated
// for fixture-ddl-db to a real Postgres; testdata/pgcheck's
// TestDDLOnPostgres holds the checks: the defaults a raw INSERT takes, the
// checks that refuse an enum's other values and a Validate bound's
// outside, the relations SET NULL clears, the expression and partial
// indexes, and the raw SQL's materialized view (D83). Set
// SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL as for
// TestProjectionMigrationsOnPostgres.
func TestDDLCreateSQLOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL to apply fixture-ddl-db's create.sql against Postgres")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-ddl-db"))
	if err != nil {
		t.Fatalf("load fixture-ddl-db: %v", err)
	}
	output, err := Generate(schema, Options{SchemaName: "fixture-ddl-db"})
	if err != nil {
		t.Fatal(err)
	}
	sqlDir := t.TempDir()
	if err := WriteDDL(output, sqlDir); err != nil {
		t.Fatal(err)
	}
	runPGCheck(t, "TestDDLOnPostgres",
		"PGCHECK_DATABASE_URL="+dsn,
		"PGCHECK_SQL_DIR="+sqlDir,
	)
}

// TestNamingCreateSQLOnPostgres applies the create.sql and drop.sql
// generated for fixture-naming-db, whose tables and columns @table and
// @column name in camel case (D85), to a real Postgres; testdata/pgcheck's
// TestNamingOnPostgres holds the checks: every name the DDL derives is the
// one Postgres holds, and the DDL works under them.
func TestNamingCreateSQLOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL to apply fixture-naming-db's create.sql against Postgres")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-naming-db"))
	if err != nil {
		t.Fatalf("load fixture-naming-db: %v", err)
	}
	output, err := Generate(schema, Options{SchemaName: "fixture-naming-db"})
	if err != nil {
		t.Fatal(err)
	}
	sqlDir := t.TempDir()
	if err := WriteDDL(output, sqlDir); err != nil {
		t.Fatal(err)
	}
	runPGCheck(t, "TestNamingOnPostgres",
		"PGCHECK_DATABASE_URL="+dsn,
		"PGCHECK_SQL_DIR="+sqlDir,
	)
}
