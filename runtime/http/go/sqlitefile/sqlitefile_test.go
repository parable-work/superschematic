package sqlitefile

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/runtime/http/go/stackconfig"
)

// TestAURLNamesItsFile: a database field's sqlite:<path> names the file,
// with or without the slashes of a URL, and anything else is refused.
func TestAURLNamesItsFile(t *testing.T) {
	for url, want := range map[string]string{
		"sqlite:/data/databases/notes_db.sqlite":   "/data/databases/notes_db.sqlite",
		"sqlite:///data/databases/notes_db.sqlite": "/data/databases/notes_db.sqlite",
		"sqlite:databases/notes_db.sqlite":         "databases/notes_db.sqlite",
	} {
		got, err := Path(url)
		if err != nil || got != want {
			t.Errorf("Path(%q) = %q, %v; want %q", url, got, err, want)
		}
	}
	for _, url := range []string{"postgres://localhost/notes", "sqlite:", ""} {
		if _, err := Path(url); err == nil {
			t.Errorf("Path(%q) names a file", url)
		}
	}
}

// TestOpenRefusesAMissingFileAndOpensOneInWAL: a migration creates the
// file, so Open refuses one that is not there, and opens one that is with
// write-ahead logging, foreign keys and the busy timeout on each
// connection.
func TestOpenRefusesAMissingFileAndOpensOneInWAL(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "notes_db.sqlite")
	if _, err := Open(stackconfig.Database{URL: "sqlite:" + file}); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("Open of a missing file = %v, want it refused", err)
	}
	if _, err := Open(stackconfig.Database{CloudSQL: &stackconfig.CloudSQL{Instance: "p:r:i"}}); err == nil {
		t.Fatal("Open of a Cloud SQL instance opened it")
	}
	// The file a migration would have made.
	create, err := sql.Open(Driver, DSN(file))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := create.Exec("CREATE TABLE note (id TEXT PRIMARY KEY, parent TEXT REFERENCES note (id))"); err != nil {
		t.Fatal(err)
	}
	_ = create.Close()

	db, err := Open(stackconfig.Database{URL: "sqlite:" + file})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for pragma, want := range map[string]string{"journal_mode": "wal", "foreign_keys": "1", "busy_timeout": "5000"} {
		var got string
		if err := db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %s, want %s", pragma, got, want)
		}
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO note (id, parent) VALUES ('a', 'missing')"); err == nil {
		t.Error("a row whose foreign key names no row was written")
	}
}
