package sqlgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// restampedProjection writes fixture-projection's migration pair at its
// stamp into dir, then changes the declaration, which takes a later stamp:
// it drops the handle column. It returns the earlier view, and the later
// one with its earlier migration replayed from dir (D128).
func restampedProjection(t *testing.T, dir string) (*ir.Schema, *DDLOutput, ProjectionView) {
	t.Helper()
	schema := loadProjectionFixture(t)
	first, err := Generate(schema, Options{SchemaName: "fixture-projection"})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteProjections(schema, first, t.TempDir(), dir); err != nil {
		t.Fatal(err)
	}
	earlier := first.Projections[0]

	td := schema.Types["AppPreference"]
	td.Projection.Migration = "20260903120000"
	kept := td.Fields[:0]
	for _, fd := range td.Fields {
		if fd.Name != "handle" {
			kept = append(kept, fd)
		}
	}
	td.Fields = kept
	second, err := Generate(schema, Options{SchemaName: "fixture-projection"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ReplayPreviousViews(second.Projections, dir); err != nil {
		t.Fatal(err)
	}
	return schema, second, earlier
}

// TestReplacementDownReplaysThePreviousView: the down migration of a view
// that took a new stamp replays the earlier stamp's up statements, without
// their comment lines, so a rollback leaves that view; a view's first
// migration still drops it (D128).
func TestReplacementDownReplaysThePreviousView(t *testing.T) {
	dir := t.TempDir()
	schema, second, earlier := restampedProjection(t, dir)
	view := second.Projections[0]
	if view.PreviousMigration != earlier.Migration {
		t.Fatalf("PreviousMigration = %q, want %q", view.PreviousMigration, earlier.Migration)
	}
	if err := WriteProjections(schema, second, t.TempDir(), dir); err != nil {
		t.Fatal(err)
	}
	up, err := os.ReadFile(filepath.Join(dir, earlier.UpFileName))
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile(filepath.Join(dir, view.DownFileName))
	if err != nil {
		t.Fatal(err)
	}
	statements := string(up)
	for strings.HasPrefix(statements, "--") {
		_, statements, _ = strings.Cut(statements, "\n")
	}
	if !strings.Contains(string(down), "-- Rolls app.preferences back to the view "+earlier.Migration+" made,") ||
		!strings.HasSuffix(strings.TrimRight(string(down), "\n"), strings.TrimRight(statements, "\n")) {
		t.Fatalf("the down migration does not replay %s:\n%s", earlier.UpFileName, down)
	}
	if !strings.Contains(string(down), "base.handle") {
		t.Fatalf("the replayed view lost the earlier column handle:\n%s", down)
	}

	firstDown, err := os.ReadFile(filepath.Join(dir, earlier.DownFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(firstDown), "DROP VIEW IF EXISTS app.preferences;") || strings.Contains(string(firstDown), "CREATE VIEW") {
		t.Fatalf("a view's first down migration must drop it:\n%s", firstDown)
	}

	// No migrations directory, or none that holds an earlier file, leaves
	// the down migration a drop.
	for _, empty := range []string{"", t.TempDir(), filepath.Join(t.TempDir(), "absent")} {
		views := append([]ProjectionView(nil), second.Projections...)
		views[0].PreviousMigration, views[0].PreviousUp = "", ""
		if err := ReplayPreviousViews(views, empty); err != nil {
			t.Fatal(err)
		}
		if views[0].PreviousUp != "" {
			t.Fatalf("migrations dir %q: replayed %q", empty, views[0].PreviousMigration)
		}
	}
}

// TestReplacementDownOnPostgres applies the earlier up, the later up and
// the later down to a real Postgres; testdata/pgcheck's
// TestProjectionDownRestoresThePreviousViewOnPostgres holds the checks:
// after the down, the view has the earlier stamp's columns again. Set
// SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL as for
// TestProjectionMigrationsOnPostgres.
func TestReplacementDownOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL to roll a re-stamped projection back on Postgres")
	}
	dir := t.TempDir()
	schema, second, earlier := restampedProjection(t, dir)
	if err := WriteDDL(second, dir); err != nil {
		t.Fatal(err)
	}
	if err := WriteProjections(schema, second, t.TempDir(), dir); err != nil {
		t.Fatal(err)
	}
	view := second.Projections[0]
	runPGCheck(t, "TestProjectionDownRestoresThePreviousViewOnPostgres",
		"PGCHECK_DATABASE_URL="+dsn,
		"PGCHECK_SQL_DIR="+dir,
		"PGCHECK_EARLIER_UP="+earlier.UpFileName,
		"PGCHECK_UP="+view.UpFileName,
		"PGCHECK_DOWN="+view.DownFileName,
	)
}
