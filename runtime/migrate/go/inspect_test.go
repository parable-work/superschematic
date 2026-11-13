package migrate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/internal/testdb"
	"github.com/parable-work/superschematic/runtime/migrate/go/sqlite"
)

// inspectTestdata holds a file built by hand, as the statements that build
// it, and its inspection, which the compiler's sqlmigrate package reads
// too: it holds the two sides to one form (D116).
const inspectTestdata = "../testdata/inspect"

var updateInspection = flag.Bool("update-inspection", false, "rewrite the inspection in ../testdata/inspect")

// inspectFile reads the SQLite file at url as the inspect command does.
func inspectFile(t *testing.T, url string) []byte {
	t.Helper()
	ctx := context.Background()
	driver, err := sqlite.OpenReadOnly(ctx, url, sqlite.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = driver.Close(ctx) }()
	inspection, err := (&migrate.Runner{Driver: driver}).Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.MarshalIndent(inspection, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

// TestInspect (D116): the inspection of a file built by hand holds its
// tables with their columns, indexes and foreign keys as SQLite's pragmas
// give them, its views and its triggers, each with the statement
// sqlite_schema keeps, and none of SQLite's own tables or the runner's. The
// read changes no byte of the file. Each <name>.sql of the testdata builds
// a file whose inspection is <name>.json. Regenerate the inspections with
// go test -run TestInspect -update-inspection .
func TestInspect(t *testing.T) {
	scripts, err := filepath.Glob(filepath.Join(inspectTestdata, "*.sql"))
	if err != nil || len(scripts) == 0 {
		t.Fatalf("no file to build in %s: %v", inspectTestdata, err)
	}
	for _, script := range scripts {
		t.Run(strings.TrimSuffix(filepath.Base(script), ".sql"), func(t *testing.T) {
			statements, err := os.ReadFile(script)
			if err != nil {
				t.Fatal(err)
			}
			url := testdb.NewSQLite(t)
			testdb.Exec(t, url, string(statements))
			before, err := os.ReadFile(url)
			if err != nil {
				t.Fatal(err)
			}
			got := inspectFile(t, url)
			golden := strings.TrimSuffix(script, ".sql") + ".json"
			if *updateInspection {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("the inspection is not %s (run go test -run TestInspect -update-inspection .):\n%s", golden, got)
			}
			after, err := os.ReadFile(url)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("inspecting the file changed it")
			}
			// AUTOINCREMENT makes sqlite_sequence, which is SQLite's own.
			if strings.Contains(string(got), `"name": "sqlite_sequence"`) {
				t.Error("the inspection holds SQLite's own table sqlite_sequence")
			}

			// The runner's state tables are no part of the file's schema.
			plan := plan(t, migrate.SQLite, "01-create")
			if _, err := newRunner(t, url).Adopt(context.Background(), plan.Model()); err != nil {
				t.Fatal(err)
			}
			if again := inspectFile(t, url); !bytes.Equal(again, want) {
				t.Fatalf("the runner's state tables are in the inspection:\n%s", again)
			}
		})
	}
}

// TestInspectReadsOnly: a file that does not exist is refused, and not
// created; a connection opened to read a file takes no write; and a
// database that is not SQLite is not inspected.
func TestInspectReadsOnly(t *testing.T) {
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "missing.db")
	for _, url := range []string{missing, "sqlite://" + missing, "file:" + missing} {
		if driver, err := sqlite.OpenReadOnly(ctx, url, sqlite.Options{}); err == nil {
			_ = driver.Close(ctx)
			t.Errorf("%s: a file that does not exist opens", url)
		}
		if _, err := os.Stat(missing); !os.IsNotExist(err) {
			t.Fatalf("%s: opening it to read created the file: %v", url, err)
		}
	}
	if _, err := sqlite.ReadOnlyDSN(":memory:"); err == nil {
		t.Error("a database in memory names a file to read")
	}

	url := testdb.NewSQLite(t)
	testdb.Exec(t, url, `CREATE TABLE note (id INTEGER PRIMARY KEY)`)
	for _, form := range []string{url, "sqlite://" + url, "file:" + url + "?mode=rwc"} {
		driver, err := sqlite.OpenReadOnly(ctx, form, sqlite.Options{})
		if err != nil {
			t.Fatalf("%s: %v", form, err)
		}
		err = driver.Transact(ctx, migrate.TxOptions{}, func(ctx context.Context, conn migrate.Conn) error {
			return conn.Exec(ctx, `INSERT INTO note DEFAULT VALUES`)
		})
		_ = driver.Close(ctx)
		if err == nil {
			t.Errorf("%s: a connection opened to read the file wrote to it", form)
		}
	}
	if count(t, url, "note") != "0" {
		t.Error("the file holds a row a read-only connection wrote")
	}

	forEachBackend(t, func(t *testing.T, db testdb.Backend) {
		if db != testdb.Postgres {
			t.Skip("a SQLite database is inspected")
		}
		if _, err := newRunner(t, testdb.New(t, db)).Inspect(ctx); err == nil || !strings.Contains(err.Error(), "inspect reads a SQLite file's catalog") {
			t.Fatalf("inspecting Postgres: %v", err)
		}
	})
}
