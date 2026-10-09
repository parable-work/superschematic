// Package sqliteconverge checks migration plans against SQLite, through
// the pure-Go modernc.org/sqlite. TestConvergenceOnSQLite in the sqlmigrate
// package writes one directory per case and runs TestConvergence and
// TestRejects here with:
//
//	SQLITECONVERGE_CASES    the directory holding the cases
//	SQLITECONVERGE_REJECTS  the directory holding the cases that must fail
//
// A case directory holds:
//
//	from.sql     sqlite/create.sql of the previous version (absent: an empty database)
//	seed.sql     rows to insert before the plan (optional)
//	plan.json    the plan from the previous version to the new one, or
//	plan-NN.json several plans, applied in name order
//	to.sql       sqlite/create.sql of the new version
//	checks.json  queries that must return 1 after the plan (optional)
//
// Each case builds two databases: one from from.sql, seed.sql and the plan's
// steps, run as the runner runs them, and one from to.sql. Their schemas
// must be the same, compared by name: the objects sqlite_schema lists, each
// table's columns (pragma table_xinfo, so a generated column with whether
// it is VIRTUAL or STORED, and each column's collation), its
// indexes (pragma index_list and index_xinfo) and its foreign keys (pragma
// foreign_key_list).
//
// Foreign keys stay on throughout, as D1 keeps them (D27, amended): a step
// that asks for them off (foreignKeysOff) or runs a statement that would
// turn them off fails the case, and so does a step after which they are
// off. TestRejects runs the cases under SQLITECONVERGE_REJECTS, laid out
// the same with a want.txt: each must fail to apply, with an error that
// contains the text of want.txt.
package sqliteconverge

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

type plan struct {
	Dialect string `json:"dialect"`
	Steps   []struct {
		Index          int      `json:"index"`
		Subject        string   `json:"subject"`
		Statements     []string `json:"statements"`
		Transactional  bool     `json:"transactional"`
		ForeignKeysOff bool     `json:"foreignKeysOff"`
	} `json:"steps"`
}

type check struct {
	Name string `json:"name"`
	SQL  string `json:"sql"`
}

// open opens a new database file on one connection with foreign keys on, as
// the runner's connection has them.
func open(t *testing.T, ctx context.Context, name string) *sql.Conn {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	exec(t, ctx, conn, "PRAGMA foreign_keys = ON")
	return conn
}

func exec(t *testing.T, ctx context.Context, conn *sql.Conn, statement string) {
	t.Helper()
	if _, err := conn.ExecContext(ctx, statement); err != nil {
		t.Fatalf("%v\n%s", err, statement)
	}
}

// execScript runs a file of statements in one call.
func execScript(t *testing.T, ctx context.Context, conn *sql.Conn, dir, name string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, string(b)); err != nil {
		t.Fatalf("apply %s: %v", name, err)
	}
}

// rows runs a query and returns its rows, each row's columns joined by a
// space; NULL is "NULL".
func rows(t *testing.T, ctx context.Context, conn *sql.Conn, query string, args ...any) []string {
	t.Helper()
	r, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
	defer func() { _ = r.Close() }()
	columns, err := r.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for r.Next() {
		values := make([]sql.NullString, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := r.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, len(values))
		for i, v := range values {
			parts[i] = "NULL"
			if v.Valid {
				parts[i] = v.String
			}
		}
		out = append(out, strings.Join(parts, " "))
	}
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// applyPlans applies the case's plans in name order.
func applyPlans(ctx context.Context, conn *sql.Conn, dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "plan*.json"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("the case has no plan")
	}
	sort.Strings(files)
	for _, file := range files {
		if err := applyPlan(ctx, conn, file); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(file), err)
		}
	}
	return nil
}

// setsForeignKeys reports whether a statement is PRAGMA foreign_keys
// with a value, which would turn foreign keys off or on. Inside a
// transaction SQLite ignores it, but D1 has none to be inside.
func setsForeignKeys(stmt string) bool {
	fields := strings.Fields(strings.ToLower(strings.NewReplacer("=", " = ", "(", " ( ").Replace(stmt)))
	return len(fields) > 2 && fields[0] == "pragma" && strings.TrimPrefix(fields[1], "main.") == "foreign_keys"
}

// applyPlan runs the plan's steps as the runner does on SQLite, each in a
// BEGIN IMMEDIATE transaction, with foreign keys on throughout: a step that
// asks for them off, or with a statement that sets them, fails, and so
// does a step after which they are off. A foreign key the step leaves
// violated fails its commit.
func applyPlan(ctx context.Context, conn *sql.Conn, file string) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var p plan
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	if p.Dialect != "sqlite" {
		return fmt.Errorf("a plan for %s", p.Dialect)
	}
	for _, step := range p.Steps {
		if !step.Transactional {
			return fmt.Errorf("step %d %s does not run in a transaction", step.Index, step.Subject)
		}
		if step.ForeignKeysOff {
			return fmt.Errorf("step %d %s asks for foreign keys off, which D1 cannot run", step.Index, step.Subject)
		}
		if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			return err
		}
		for _, stmt := range step.Statements {
			err := error(nil)
			if setsForeignKeys(stmt) {
				err = fmt.Errorf("it sets foreign_keys, which D1 cannot run")
			} else {
				_, err = conn.ExecContext(ctx, stmt)
			}
			if err != nil {
				_, _ = conn.ExecContext(ctx, "ROLLBACK")
				return fmt.Errorf("step %d %s: %w\n%s", step.Index, step.Subject, err, stmt)
			}
		}
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			return fmt.Errorf("step %d %s: commit: %w", step.Index, step.Subject, err)
		}
		var on int
		if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&on); err != nil {
			return err
		}
		if on != 1 {
			return fmt.Errorf("step %d %s leaves foreign keys off", step.Index, step.Subject)
		}
	}
	return nil
}

// catalog reads the schema of a database as sorted lines, compared by name.
// Column order is left out, as ADD COLUMN puts a column last; the order of
// an index's columns is kept.
func catalog(t *testing.T, ctx context.Context, conn *sql.Conn) []string {
	t.Helper()
	var out []string
	var tables []string
	for _, line := range rows(t, ctx, conn, `SELECT type, name, tbl_name FROM sqlite_schema WHERE name NOT LIKE 'sqlite\_stat%' ESCAPE '\'`) {
		out = append(out, "object "+line)
		if parts := strings.Fields(line); parts[0] == "table" {
			tables = append(tables, parts[1])
		}
	}
	for _, table := range tables {
		for _, col := range rows(t, ctx, conn, `SELECT name, type, "notnull", dflt_value, pk, hidden FROM pragma_table_xinfo(?)`, table) {
			name := strings.Fields(col)[0]
			out = append(out, fmt.Sprintf("column %s.%s collate %s", table, col, collation(t, ctx, conn, table, name)))
		}
		for _, idx := range rows(t, ctx, conn, `SELECT name, "unique", origin, partial FROM pragma_index_list(?)`, table) {
			name := strings.Fields(idx)[0]
			columns := rows(t, ctx, conn, `SELECT name, coll, "desc" FROM pragma_index_xinfo(?) WHERE key = 1 ORDER BY seqno`, name)
			out = append(out, fmt.Sprintf("index %s.%s (%s)", table, idx, strings.Join(columns, ", ")))
		}
		fks := map[string][]string{}
		for _, fk := range rows(t, ctx, conn, `SELECT id, "table", "from", "to", on_update, on_delete, match FROM pragma_foreign_key_list(?) ORDER BY id, seq`, table) {
			parts := strings.SplitN(fk, " ", 2)
			fks[parts[0]] = append(fks[parts[0]], parts[1])
		}
		for _, fk := range fks {
			out = append(out, fmt.Sprintf("foreign key %s %s", table, strings.Join(fk, "; ")))
		}
	}
	sort.Strings(out)
	return out
}

// collation is the collation a column compares with. SQLite reports a
// column's collation only through an index over it, so one is built and
// rolled back.
func collation(t *testing.T, ctx context.Context, conn *sql.Conn, table, column string) string {
	t.Helper()
	quote := func(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }
	exec(t, ctx, conn, "SAVEPOINT probe")
	exec(t, ctx, conn, "CREATE INDEX _collation_probe ON "+quote(table)+" ("+quote(column)+")")
	coll := rows(t, ctx, conn, `SELECT coll FROM pragma_index_xinfo('_collation_probe') WHERE key = 1`)
	exec(t, ctx, conn, "ROLLBACK TO probe")
	exec(t, ctx, conn, "RELEASE probe")
	return strings.Join(coll, ",")
}

func compare(t *testing.T, planned, created []string) {
	t.Helper()
	var missing, extra []string
	for _, line := range created {
		if !slices.Contains(planned, line) {
			missing = append(missing, line)
		}
	}
	for _, line := range planned {
		if !slices.Contains(created, line) {
			extra = append(extra, line)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		t.Errorf("the schemas differ:\nonly from to.sql:\n  %s\nonly from the plan:\n  %s",
			strings.Join(missing, "\n  "), strings.Join(extra, "\n  "))
	}
}

func TestConvergence(t *testing.T) {
	casesDir := os.Getenv("SQLITECONVERGE_CASES")
	if casesDir == "" {
		t.Skip("SQLITECONVERGE_CASES is not set; run through TestConvergenceOnSQLite")
	}
	entries, err := os.ReadDir(casesDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(casesDir, entry.Name())
		t.Run(entry.Name(), func(t *testing.T) {
			planned := open(t, ctx, "planned.db")
			execScript(t, ctx, planned, dir, "from.sql")
			execScript(t, ctx, planned, dir, "seed.sql")
			if err := applyPlans(ctx, planned, dir); err != nil {
				t.Fatal(err)
			}
			if violations := rows(t, ctx, planned, "PRAGMA foreign_key_check"); len(violations) > 0 {
				t.Errorf("foreign_key_check after the plan: %v", violations)
			}
			if result := rows(t, ctx, planned, "PRAGMA integrity_check"); !slices.Equal(result, []string{"ok"}) {
				t.Errorf("integrity_check after the plan: %v", result)
			}
			var checks []check
			if b, err := os.ReadFile(filepath.Join(dir, "checks.json")); err == nil {
				if err := json.Unmarshal(b, &checks); err != nil {
					t.Fatal(err)
				}
			}
			for _, c := range checks {
				if got := rows(t, ctx, planned, c.SQL); !slices.Equal(got, []string{"1"}) {
					t.Errorf("check %s failed (%v):\n%s", c.Name, got, c.SQL)
				}
			}

			created := open(t, ctx, "created.db")
			execScript(t, ctx, created, dir, "to.sql")
			compare(t, catalog(t, ctx, planned), catalog(t, ctx, created))
		})
	}
}

// TestRejects shows that applying a plan fails on a step that turns
// foreign keys off: each case under SQLITECONVERGE_REJECTS must fail to
// apply, with an error that contains the text of its want.txt.
func TestRejects(t *testing.T) {
	casesDir := os.Getenv("SQLITECONVERGE_REJECTS")
	if casesDir == "" {
		t.Skip("SQLITECONVERGE_REJECTS is not set; run through TestConvergenceOnSQLite")
	}
	entries, err := os.ReadDir(casesDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(casesDir, entry.Name())
		t.Run(entry.Name(), func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(dir, "want.txt"))
			if err != nil {
				t.Fatal(err)
			}
			want := strings.TrimSpace(string(b))
			conn := open(t, ctx, "planned.db")
			execScript(t, ctx, conn, dir, "from.sql")
			execScript(t, ctx, conn, dir, "seed.sql")
			if err := applyPlans(ctx, conn, dir); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("apply = %v, want an error containing %q", err, want)
			}
		})
	}
}
