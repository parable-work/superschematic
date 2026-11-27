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
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// sqliteDriverVersion is the modernc.org/sqlite a generated module's test
// opens a SQLite file with: the HTTP runtime's, whose sqlitefile a Go
// server opens its database with (D116).
const sqliteDriverVersion = "v1.60.1"

// TestTableOperationsGoServerOnSQLite builds fixture-tracker-db with the
// sqlite dialect listed first and fixture-tracker-api over it, as
// build-all does (D116): the API's Deps.DB is then the ORM's
// SQLiteDatabase, a DatabaseInterface as the Postgres Database is, and its
// table operations are served from the Go repositories on SQLite. It runs
// the scenario TestTableOperationsGoServerServesItsTables runs on
// Postgres, case for case, over a file the build's SQLite DDL creates,
// through the generated routes over HTTP: every kind of operation, the
// path's parents, the soft delete, the audit stamps, the defaults, an
// update's absent, null and empty body, the refusals (404, 409, the 400 at
// a member, 401, 403), which a SQLite constraint answers as Postgres's
// does, a list's page, sort, search and filters of every kind, and a row
// rule.
//
// It runs outside -short, and needs no database.
func TestTableOperationsGoServerOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Go server's table operations on SQLite in -short mode")
	}
	paths := testpaths.Local(t)
	outputRoot := t.TempDir()
	configure := func(service string, cfg *schemaconfig.SchemaConfig) {
		if service != "fixture-tracker-db" {
			return
		}
		sql, _ := cfg.Outputs["sql"].(map[string]any)
		if sql == nil {
			sql = map[string]any{"enabled": true}
			cfg.Outputs["sql"] = sql
		}
		sql["dialects"] = []any{"sqlite", "postgres"}
	}
	for _, service := range []string{"fixture-tracker-db", "fixture-tracker-api"} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		configure(service, cfg)
		if _, err := Run(schema, cfg, Options{
			OutputRoot: outputRoot,
			Paths:      paths,
			Naming:     naming.Default(),
			Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
			LoadDependency: func(name string) (*ir.Schema, error) {
				return loader.LoadService(filepath.Join(tsFixtures, name))
			},
			DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
				_, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, name))
				if err != nil {
					return nil, false
				}
				configure(name, cfg)
				return cfg, true
			},
		}); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}

	apiDir := APIDir(outputRoot, "fixture-tracker-api")
	ddl := filepath.Join(SQLDir(outputRoot, "fixture-tracker-db"), SQLiteSubdir, "create.sql")
	create, err := os.ReadFile(ddl)
	if err != nil {
		t.Fatalf("the build wrote no SQLite DDL for fixture-tracker-db: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(apiDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		filepath.Join("testdata", "sqlite.sql"): string(create),
		"table_operations_test.go":              tableOperationsTest,
		"table_operations_sqlite_test.go":       tableOperationsOnSQLite,
	} {
		if err := os.WriteFile(filepath.Join(apiDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	var out []byte
	for _, args := range [][]string{
		{"mod", "edit", "-require=modernc.org/sqlite@" + sqliteDriverVersion},
		{"mod", "tidy"},
		{"vet", "./..."},
		{"test", "-count=1", "-v", "-run", "^TestTableOperations$", "./..."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		cmd.Env = append(cmd.Environ(), env...)
		var err error
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	t.Logf("generated API tests on SQLite:\n%s", out)
	for _, name := range tableOperationCases {
		if want := "--- PASS: TestTableOperations/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the generated module's test on SQLite did not pass %q", name)
		}
	}
}

// tableOperationsOnSQLite runs in the generated module of
// fixture-tracker-api beside tableOperationsTest, whose openDatabase it
// points at a SQLite file the build's SQLite DDL creates, opened as a
// generated Go server opens its database's file (SQLiteDSN).
const tableOperationsOnSQLite = `package fixturetrackerapi_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	orm "example.com/schemas/orm/fixture-tracker-db"
)

func init() {
	openSQLite = func(t *testing.T) (tableReads, orm.DatabaseInterface) {
		t.Helper()
		ctx := context.Background()
		ddl, err := os.ReadFile("testdata/sqlite.sql")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := sql.Open("sqlite", orm.SQLiteDSN(filepath.Join(t.TempDir(), "tracker.sqlite")))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.ExecContext(ctx, string(ddl)); err != nil {
			t.Fatalf("apply the SQLite DDL: %v", err)
		}
		db, err := orm.OpenSQLite(ctx, raw)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(db.Close)
		return tableReads{
			// A REAL that holds a whole number reads as Postgres's ::text
			// writes it, 0 and not 0.0.
			text: func(ctx context.Context, table, column, id string) (*string, error) {
				var value *string
				err := raw.QueryRowContext(ctx, "SELECT CASE WHEN typeof("+column+") = 'real' AND "+column+" = CAST("+column+" AS INTEGER) THEN CAST(CAST("+column+" AS INTEGER) AS TEXT) ELSE CAST("+column+" AS TEXT) END FROM "+table+" WHERE id = ?1", id).Scan(&value)
				return value, err
			},
			nullTitles: func(ctx context.Context, team, column string) ([]string, error) {
				rows, err := raw.QueryContext(ctx, "SELECT title FROM issue WHERE team_id = ?1 AND "+column+" IS NULL ORDER BY id", team)
				if err != nil {
					return nil, err
				}
				defer rows.Close()
				var titles []string
				for rows.Next() {
					var title string
					if err := rows.Scan(&title); err != nil {
						return nil, err
					}
					titles = append(titles, title)
				}
				return titles, rows.Err()
			},
			count: func(ctx context.Context, table, id string) (int, error) {
				var rows int
				err := raw.QueryRowContext(ctx, "SELECT count(*) FROM "+table+" WHERE id = ?1", id).Scan(&rows)
				return rows, err
			},
		}, db
	}
}
`
