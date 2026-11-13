package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/internal/testdb"
)

// TestInspectCommand (D116): inspect prints a SQLite file's catalog as
// JSON and writes nothing to the file, not the runner's state tables
// either; it refuses a file that does not exist without creating it, and a
// URL that names no SQLite file is a usage error.
func TestInspectCommand(t *testing.T) {
	url := testdb.NewSQLite(t)
	testdb.Exec(t, url,
		`CREATE TABLE note (id INTEGER PRIMARY KEY, body TEXT NOT NULL DEFAULT '')`,
		`CREATE VIEW recent AS SELECT id FROM note`,
	)
	before, err := os.ReadFile(url)
	if err != nil {
		t.Fatal(err)
	}
	out := invoke(t, nil, "inspect", "--database-url", url).expect(t, exitOK)
	var inspection migrate.Inspection
	if err := json.Unmarshal([]byte(out.stdout), &inspection); err != nil {
		t.Fatalf("the output is no inspection: %v\n%s", err, out.stdout)
	}
	if inspection.Version != migrate.InspectVersion || len(inspection.Tables) != 1 || inspection.Tables[0].Name != "note" ||
		len(inspection.Tables[0].Columns) != 2 || len(inspection.Views) != 1 || inspection.Views[0].Name != "recent" {
		t.Fatalf("inspection = %+v", inspection)
	}
	after, err := os.ReadFile(url)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("inspect changed the file")
	}
	invoke(t, map[string]string{"DATABASE_URL": url}, "inspect").expect(t, exitOK, `"name": "note"`)

	missing := filepath.Join(t.TempDir(), "missing.db")
	invoke(t, nil, "inspect", "--database-url", missing).expect(t, exitFailed, "no such file")
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("inspect created the file it was to read: %v", err)
	}

	invoke(t, nil, "inspect", "--database-url", "postgres://localhost/shop").expect(t, exitUsage, "inspect reads a SQLite file")
	invoke(t, nil, "inspect").expect(t, exitUsage, "no database")
}
