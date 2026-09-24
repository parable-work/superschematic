package sqlgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestProjectionMigrationsOnPostgres applies the SQL generated for the
// fixture-projection service to a real Postgres and checks the view's
// behaviour there: create.sql and drop.sql apply, the down migration drops
// the view, the up migration (run twice) creates it as the view owner with
// security_barrier, the view raises without its required settings, serves
// only the rows its rules admit with one row per slot, hides the base
// tables from a reader, and has the Arrow schema's columns in order.
//
// The checks live in testdata/pgcheck, a module of their own, so the
// Postgres driver stays out of this module. Set
// SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL to a URL whose role may create
// databases and roles (a throwaway postgres:15-alpine container's postgres
// user is enough); the test creates and drops its own database and roles.
func TestProjectionMigrationsOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL to run the projection migrations against Postgres")
	}
	owner := fmt.Sprintf("projection_owner_%d", time.Now().UnixNano())
	schema := loadProjectionFixture(t)
	output, err := Generate(schema, Options{SchemaName: "fixture-projection", ViewOwner: owner})
	if err != nil {
		t.Fatal(err)
	}
	sqlDir := t.TempDir()
	if err := WriteDDL(output, sqlDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteProjections(schema, output, sqlDir, sqlDir); err != nil {
		t.Fatal(err)
	}

	module := t.TempDir()
	if err := os.CopyFS(module, os.DirFS(filepath.Join("testdata", "pgcheck"))); err != nil {
		t.Fatal(err)
	}
	view := output.Projections[0]
	cmd := exec.Command("go", "test", "-count=1", "-v", "./...")
	cmd.Dir = module
	cmd.Env = append(os.Environ(),
		"GOFLAGS=-mod=readonly",
		"PGCHECK_DATABASE_URL="+dsn,
		"PGCHECK_SQL_DIR="+sqlDir,
		"PGCHECK_UP="+view.UpFileName,
		"PGCHECK_DOWN="+view.DownFileName,
		"PGCHECK_ARROW="+filepath.Join(sqlDir, ProjectionsSubdir, view.Relation+".arrow.json"),
		"PGCHECK_VIEW_OWNER="+owner,
	)
	out, err := cmd.CombinedOutput()
	t.Logf("pgcheck:\n%s", out)
	if err != nil {
		t.Fatalf("pgcheck failed: %v", err)
	}
	if !strings.Contains(string(out), "--- PASS: TestProjectionOnPostgres") {
		t.Fatal("pgcheck did not run its test")
	}
}
