package sqlmigrate

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestConvergenceOnPostgres applies plans to a real Postgres and checks
// that they converge (D27, Testing): for each plan case (A, B), create.sql
// of A, then rows seeded into it, then the plan from A to B, leaves the
// same catalog as create.sql of B, and the seeded rows are still there
// unless a step deleted them; for every fixture, the plan from an empty
// database leaves the same catalog as its create.sql, and the primary key
// and unique constraints have the names the model gives them; and the
// runner's vectors, applied in order, leave the catalog of their last
// version's create.sql.
//
// The checks live in testdata/pgconverge, a module of its own, so the
// Postgres driver stays out of this module. Set
// SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL to a URL whose role may create
// databases; the test creates and drops its own.
func TestConvergenceOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_SQLGEN_TEST_DATABASE_URL to apply the plans to Postgres")
	}
	cases := t.TempDir()
	for _, pc := range planCases {
		if pc.noConverge != "" {
			t.Logf("%s: not applied: %s", pc.name, pc.noConverge)
			continue
		}
		writePlanCase(t, filepath.Join(cases, pc.name), pc)
	}
	for _, dir := range modelFixtures {
		schema, err := loader.LoadService(dir)
		if err != nil {
			t.Fatal(err)
		}
		writeEmptyCase(t, filepath.Join(cases, "empty-"+filepath.Base(dir)), schema, sqlgen.Options{SchemaName: filepath.Base(dir)})
	}
	writeEmptyCase(t, filepath.Join(cases, "empty-names"), namesSchema(), sqlgen.Options{SchemaName: "names"})
	// The runner's vectors, each case's plans one after another.
	for name, run := range planVectors(t) {
		dir := filepath.Join(cases, "vectors-"+name)
		for i, plan := range run.plans {
			writeJSON(t, filepath.Join(dir, fmt.Sprintf("plan-%02d.json", i+1)), plan)
		}
		writeFile(t, filepath.Join(dir, "to.sql"), []byte(createSQL(t, run.schema, sqlgen.Options{SchemaName: "ledger-db"})))
	}

	module := t.TempDir()
	if err := os.CopyFS(module, os.DirFS(filepath.Join("testdata", "pgconverge"))); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "-count=1", "-v", "-run", "^TestConvergence$", "./...")
	cmd.Dir = module
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "PGCONVERGE_DATABASE_URL="+dsn, "PGCONVERGE_CASES="+cases)
	out, err := cmd.CombinedOutput()
	t.Logf("pgconverge:\n%s", out)
	if err != nil {
		t.Fatalf("pgconverge failed: %v", err)
	}
	if !strings.Contains(string(out), "--- PASS: TestConvergence") {
		t.Fatal("pgconverge did not run TestConvergence")
	}
}

// createSQL renders create.sql for a schema, or "" when it has no tables.
func createSQL(t *testing.T, schema *ir.Schema, opts sqlgen.Options) string {
	t.Helper()
	out, err := sqlgen.Generate(schema, opts)
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		return ""
	}
	dir := t.TempDir()
	if err := sqlgen.WriteDDL(out, dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, b)
}

// writePlanCase writes one plan case for pgconverge: both create.sql files,
// the plan, and, unless the plan fails on any row, rows to seed and the
// checks that they survive.
func writePlanCase(t *testing.T, dir string, pc planCase) {
	fromSchema, toSchema, fromOpts, toOpts := pc.versions(t)
	plan := pc.plan(t)
	writeJSON(t, filepath.Join(dir, "plan.json"), plan)
	writeFile(t, filepath.Join(dir, "to.sql"), []byte(createSQL(t, toSchema, toOpts)))
	if pc.fromEmpty {
		return
	}
	writeFile(t, filepath.Join(dir, "from.sql"), []byte(createSQL(t, fromSchema, fromOpts)))
	if pc.noSeed != "" {
		t.Logf("%s: no rows seeded: %s", pc.name, pc.noSeed)
		return
	}
	from, to := pc.models(t)
	r, err := resolveRenames(from, to, pc.renames)
	if err != nil {
		t.Fatal(err)
	}
	seed, checks := seedRows(t, from, to, r)
	t.Logf("%s: %d rows seeded, %d checks", pc.name, strings.Count(seed, "INSERT"), len(checks))
	writeFile(t, filepath.Join(dir, "seed.sql"), []byte(seed))
	writeJSON(t, filepath.Join(dir, "checks.json"), checks)
}

// writeEmptyCase writes the plan from an empty database to a schema's
// model, with the names of its primary key and unique constraints.
func writeEmptyCase(t *testing.T, dir string, schema *ir.Schema, opts sqlgen.Options) {
	model, err := BuildModel(schema, opts, Postgres)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Diff(nil, model, Options{})
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(dir, "plan.json"), plan)
	writeFile(t, filepath.Join(dir, "to.sql"), []byte(createSQL(t, schema, opts)))
	type name struct {
		Table string `json:"table"`
		Name  string `json:"name"`
	}
	var names []name
	for _, table := range model.Tables {
		if table.PrimaryKey != nil {
			names = append(names, name{table.Name, table.PrimaryKey.Name})
		}
		for _, u := range table.Uniques {
			names = append(names, name{table.Name, u.Name})
		}
	}
	writeJSON(t, filepath.Join(dir, "constraints.json"), names)
}

type seedCheck struct {
	Name string `json:"name"`
	SQL  string `json:"sql"`
}

// seedRows inserts one row into every entity and join table of from, with
// a value in every column it can give one, and returns the checks that
// each row is still there after the plan, under the new names, with the
// values of the columns the plan keeps and does not retype. A table that
// becomes versioned must have one history image per row.
func seedRows(t *testing.T, from, to *Model, r *renames) (string, []seedCheck) {
	t.Helper()
	keys := map[string]string{} // table -> literal of its seeded row's key
	values := map[string]map[string]string{}
	var order []*Table
	pending := map[string]*Table{}
	for _, table := range from.Tables {
		if table.Kind != TableHistory {
			pending[table.Name] = table
		}
	}
	for progress := true; progress; {
		progress = false
		names := make([]string, 0, len(pending))
		for name := range pending {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			table := pending[name]
			row, ok := seedRow(table, len(order)+1, keys)
			if !ok {
				continue
			}
			delete(pending, name)
			values[name] = row
			if len(table.PrimaryKey.Columns) == 1 {
				keys[name] = row[table.PrimaryKey.Columns[0]]
			}
			order = append(order, table)
			progress = true
		}
	}

	var sql strings.Builder
	var checks []seedCheck
	toTables := tablesByName(to)
	for _, table := range order {
		row := values[table.Name]
		var columns, literals []string
		for _, col := range table.Columns {
			if v, ok := row[col.Name]; ok {
				columns = append(columns, q(col.Name))
				literals = append(literals, v)
			}
		}
		fmt.Fprintf(&sql, "INSERT INTO %s (%s) VALUES (%s);\n", q(table.Name), strings.Join(columns, ", "), strings.Join(literals, ", "))

		tt := toTables[r.table(table.Name)]
		if tt == nil {
			continue
		}
		var conds []string
		for _, col := range table.Columns {
			v, ok := row[col.Name]
			newCol := columnNamed(tt, r.column(table.Name, col.Name))
			if !ok || newCol == nil || newCol.Type != col.Type {
				continue
			}
			conds = append(conds, fmt.Sprintf("%s IS NOT DISTINCT FROM %s", q(newCol.Name), v))
		}
		checks = append(checks, seedCheck{
			Name: "row of " + table.Name + " survives",
			SQL:  fmt.Sprintf("SELECT count(*) = 1 FROM %s WHERE %s", q(tt.Name), strings.Join(conds, " AND ")),
		})
		if tt.History != "" && table.History == "" {
			key := q(tt.PrimaryKey.Columns[0])
			checks = append(checks, seedCheck{
				Name: "history of " + tt.Name + " is seeded",
				SQL: fmt.Sprintf("SELECT count(*) = 1 FROM %s h JOIN %s t ON h.%s = t.%s AND h._version = t._version AND h.operation = 'INSERT'",
					q(tt.History), q(tt.Name), key, key),
			})
			for _, col := range tt.HistoryExclude {
				checks = append(checks, seedCheck{
					Name: "history of " + tt.Name + " leaves out " + col,
					SQL:  fmt.Sprintf("SELECT bool_and(NOT (data ? %s)) FROM %s", literal(col), q(tt.History)),
				})
			}
		}
	}
	return sql.String(), checks
}

// seedRow picks a value for every column of table it can: a foreign key
// takes the seeded key of the table it references, which must be seeded
// first unless the column is nullable.
func seedRow(table *Table, n int, keys map[string]string) (map[string]string, bool) {
	row := map[string]string{}
	refs := map[string]string{}
	for _, fk := range table.ForeignKeys {
		if len(fk.Columns) == 1 {
			refs[fk.Columns[0]] = fk.RefTable
		}
	}
	for i, col := range table.Columns {
		if col.Generated != "" {
			continue
		}
		if ref, ok := refs[col.Name]; ok {
			if key, ok := keys[ref]; ok {
				row[col.Name] = key
			} else if !col.Nullable {
				return nil, false
			}
			continue
		}
		v, ok := seedValue(col.Type, n, i)
		if !ok {
			if !col.Nullable && col.Default == "" {
				return nil, false
			}
			continue
		}
		row[col.Name] = v
	}
	return row, true
}

// seedValue is a literal of a column type, or false for a type it has none
// for.
func seedValue(sqlType string, n, i int) (string, bool) {
	t := parsePGType(sqlType)
	var v string
	switch {
	case t.array:
		v = "{}"
	case t.base == "UUID":
		// Every UUID of a row is its key, so a foreign key added over a
		// UUID column references a row that exists.
		v = fmt.Sprintf("%08x-0000-4000-8000-000000000000", n)
	case isPGText(t.base):
		// A UUID's text, which a cast to UUID accepts.
		v = fmt.Sprintf("%08x-0000-4000-8000-%012x", n, i)
		if len(t.mods) > 0 && t.mods[0] < len(v) {
			v = v[:t.mods[0]]
		}
	case pgIntegerBytes[t.base] > 0, isPGFloat(t.base), t.base == "NUMERIC":
		v = "7"
	case t.base == "BOOLEAN":
		v = "true"
	case t.base == "TIMESTAMPTZ":
		v = "2026-01-02 03:04:05+00"
	case t.base == "TIMESTAMP":
		v = "2026-01-02 03:04:05"
	case t.base == "DATE":
		v = "2026-01-02"
	case t.base == "TIME":
		v = "03:04:05"
	case t.base == "INTERVAL":
		v = "1 day"
	case t.base == "JSONB", t.base == "JSON":
		v = `{"k": 1}`
	case t.base == "INET":
		v = "10.0.0.1"
	case t.base == "LTREE":
		v = "a.b"
	default:
		return "", false
	}
	return literal(v) + "::" + sqlType, true
}
