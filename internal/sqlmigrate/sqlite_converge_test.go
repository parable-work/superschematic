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
)

// TestConvergenceOnSQLite applies plans to SQLite and checks that they
// converge (D27, Testing), as TestConvergenceOnPostgres does for Postgres:
// for each SQLite plan case (A, B), sqlite/create.sql of A, then rows seeded
// into it, then the plan from A to B, leaves the same schema as
// sqlite/create.sql of B, and the seeded rows are still there unless a step
// deleted them, with the lists a case seeds (listRows) holding the JSON
// text it expects; for each case with contract steps, sqlite/create.sql of A,
// the rows and the plan's expand steps leave the schema of the plan from an
// empty database to the plan's expandedModel (D27, amended); for every
// SQLite fixture, the plan from an empty database leaves the same schema as
// its create.sql; and the runner's SQLite vectors, applied in order, a plan
// the next one supersedes up to its contract, leave the schema of their
// last version's create.sql. The steps run as the runner runs them, with
// foreign keys on throughout, as D1 keeps them (D27, amended): a step that
// asks for them off fails, and a step's foreign keys are checked at its
// commit. The cases of sqliteRejects must fail to apply, which shows that
// the checks catch a step that turns them off, and that a step that drops
// rows a RESTRICT key protects needs its checks deferred.
//
// The checks live in testdata/sqliteconverge, a module of its own, so the
// SQLite driver stays out of this module. It needs no database server.
func TestConvergenceOnSQLite(t *testing.T) {
	cases := t.TempDir()
	covered := map[string]bool{}
	for _, pc := range sqlitePlanCases() {
		for what := range writeSQLitePlanCase(t, filepath.Join(cases, pc.golden()), pc) {
			covered[what] = true
		}
		writeListRows(t, filepath.Join(cases, pc.golden()), pc.lists)
		writeExtraRows(t, filepath.Join(cases, pc.golden()), pc.rows)
		if plan := pc.plan(t); plan.Expanded != "" {
			from, _ := pc.models(t)
			dir := filepath.Join(cases, "expanded-"+pc.golden())
			writeExpandedCase(t, dir, pc, plan, sqliteCreateSQL(t, from), sqliteSeeds)
			writeListRows(t, dir, pc.lists)
			writeExtraRows(t, dir, pc.rows)
		}
	}
	// A rebuild of a parent must keep the rows of the tables that reference
	// it with every ON DELETE action, and of a grandchild: some case must
	// check each.
	for _, what := range []string{"CASCADE", "SET NULL", "RESTRICT", "NO ACTION", "grandchild", "itself"} {
		if !covered[what] {
			t.Errorf("no case rebuilds a table that a seeded row references through %s", what)
		}
	}
	for name, schema := range sqliteModelFixtures(t) {
		model := sqliteModel(t, name, schema)
		plan, err := Diff(nil, model, Options{})
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(cases, "empty-"+name)
		writeJSON(t, filepath.Join(dir, "plan.json"), plan)
		writeFile(t, filepath.Join(dir, "to.sql"), []byte(sqliteCreateSQL(t, model)))
	}
	for name, run := range planVectors(t) {
		if run.dialect != SQLite {
			continue
		}
		dir := filepath.Join(cases, "vectors-"+name)
		for i := range run.plans {
			writeJSON(t, filepath.Join(dir, fmt.Sprintf("plan-%02d.json", i+1)), run.applied(i))
		}
		writeFile(t, filepath.Join(dir, "to.sql"), []byte(sqliteCreateSQL(t, run.model)))
	}

	rejects := t.TempDir()
	writeRejects(t, cases, rejects)

	module := t.TempDir()
	if err := os.CopyFS(module, os.DirFS(filepath.Join("testdata", "sqliteconverge"))); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "-count=1", "-v", "-run", "^(TestConvergence|TestRejects)$", "./...")
	cmd.Dir = module
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "CGO_ENABLED=0",
		"SQLITECONVERGE_CASES="+cases, "SQLITECONVERGE_REJECTS="+rejects)
	out, err := cmd.CombinedOutput()
	t.Logf("sqliteconverge:\n%s", out)
	if err != nil {
		t.Fatalf("sqliteconverge failed: %v", err)
	}
	for _, test := range []string{"TestConvergence", "TestRejects"} {
		if !strings.Contains(string(out), "--- PASS: "+test+" ") {
			t.Fatalf("sqliteconverge did not run %s", test)
		}
	}
}

// sqliteRejects are the cases sqliteconverge's TestRejects must see fail to
// apply: a plan case's plan, run on its from.sql and seed.sql, with each
// step that defers the foreign key checks edited. Turning foreign keys off,
// by the step's flag or by a statement, is refused; and without the
// deferral, a rebuild or a drop finds rows a RESTRICT key protects and
// fails at once.
var sqliteRejects = []struct {
	name, from, want string
	edit             func(*Step)
}{
	{"foreign-keys-off", "rebuild-referenced-table", "asks for foreign keys off",
		func(step *Step) { step.ForeignKeysOff = true }},
	{"pragma-foreign-keys-off", "rebuild-referenced-table", "sets foreign_keys",
		func(step *Step) { step.Statements = append([]string{"PRAGMA foreign_keys = OFF"}, step.Statements...) }},
	{"rebuild-not-deferred", "rebuild-self-referencing-table", restrictFails,
		func(step *Step) { step.Statements = step.Statements[1:] }},
	{"drop-not-deferred", "drop-tables-in-restrict-cycle", restrictFails,
		func(step *Step) { step.Statements = step.Statements[1:] }},
}

// restrictFails is how a DROP TABLE fails when a RESTRICT action finds a
// row its key protects: SQLITE_CONSTRAINT_TRIGGER (1811), the error of the
// trigger SQLite runs for the action.
const restrictFails = "FOREIGN KEY constraint failed (1811)\nDROP TABLE"

// writeRejects writes sqliteRejects into dir, taking each plan case's
// from.sql and seed.sql from cases.
func writeRejects(t *testing.T, cases, dir string) {
	t.Helper()
	byName := map[string]planCase{}
	for _, pc := range sqlitePlanCases() {
		byName[pc.name] = pc
	}
	for _, r := range sqliteRejects {
		pc := byName[r.from]
		plan := pc.plan(t)
		edited := false
		for _, step := range plan.Steps {
			if len(step.Statements) > 0 && step.Statements[0] == sqliteDeferForeignKeys {
				r.edit(step)
				edited = true
			}
		}
		if !edited {
			t.Fatalf("%s: %s has no step that defers the foreign key checks", r.name, r.from)
		}
		writeJSON(t, filepath.Join(dir, r.name, "plan.json"), plan)
		for _, name := range []string{"from.sql", "seed.sql"} {
			b, err := os.ReadFile(filepath.Join(cases, pc.golden(), name))
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(dir, r.name, name), b)
		}
		writeFile(t, filepath.Join(dir, r.name, "want.txt"), []byte(r.want+"\n"))
	}
}

// sqliteCreateSQL is SQLite's create.sql of a model.
func sqliteCreateSQL(t *testing.T, model *Model) string {
	t.Helper()
	script, err := CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	return script
}

// writeSQLitePlanCase writes one SQLite plan case for sqliteconverge, as
// writePlanCase does for Postgres. It reports how seeded rows reference a
// table the case rebuilds: through the ON DELETE action of each foreign
// key to it, "itself" for a key of the table to itself, and "grandchild"
// for a table that references one that references it.
func writeSQLitePlanCase(t *testing.T, dir string, pc planCase) map[string]bool {
	t.Helper()
	from, to := pc.models(t)
	plan := pc.plan(t)
	writeJSON(t, filepath.Join(dir, "plan.json"), plan)
	writeFile(t, filepath.Join(dir, "to.sql"), []byte(sqliteCreateSQL(t, to)))
	if pc.fromEmpty {
		return nil
	}
	writeFile(t, filepath.Join(dir, "from.sql"), []byte(sqliteCreateSQL(t, from)))
	if pc.noSeed != "" {
		t.Logf("%s: no rows seeded: %s", pc.golden(), pc.noSeed)
		return nil
	}
	r, err := resolveRenames(from, to, pc.renames)
	if err != nil {
		t.Fatal(err)
	}
	seed, checks := seedRows(t, from, to, r, sqliteSeeds)
	writeFile(t, filepath.Join(dir, "seed.sql"), []byte(seed))
	writeJSON(t, filepath.Join(dir, "checks.json"), checks)

	rebuilt := map[string]bool{}
	for _, step := range plan.Steps {
		if step.Op == "copyTable" {
			rebuilt[strings.TrimPrefix(step.Subject, "table/")] = true
		}
	}
	covered := map[string]bool{}
	children := map[string]bool{}
	for _, table := range from.Tables {
		for _, fk := range table.ForeignKeys {
			switch {
			case !rebuilt[r.table(fk.RefTable)]:
			case fk.RefTable == table.Name:
				covered["itself"] = true
			default:
				covered[fk.OnDelete] = true
				children[table.Name] = true
			}
		}
	}
	for _, table := range from.Tables {
		for _, fk := range table.ForeignKeys {
			if children[fk.RefTable] && fk.RefTable != table.Name {
				covered["grandchild"] = true
			}
		}
	}
	return covered
}

// writeExtraRows adds a case's extraRows to the seed.sql and checks.json in
// dir.
func writeExtraRows(t *testing.T, dir string, rows extraRows) {
	t.Helper()
	if len(rows.seed) == 0 && len(rows.checks) == 0 {
		return
	}
	seed, err := os.ReadFile(filepath.Join(dir, "seed.sql"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "checks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var checks []seedCheck
	if err := json.Unmarshal(b, &checks); err != nil {
		t.Fatal(err)
	}
	sql := string(seed)
	for _, statement := range rows.seed {
		sql += statement + ";\n"
	}
	writeFile(t, filepath.Join(dir, "seed.sql"), []byte(sql))
	writeJSON(t, filepath.Join(dir, "checks.json"), append(checks, rows.checks...))
}

// writeListRows adds a case's listRows to the seed.sql and checks.json in
// dir: a row of product per list, and a check that its list holds the JSON
// text the case expects after the plan.
func writeListRows(t *testing.T, dir string, rows listRows) {
	t.Helper()
	if len(rows) == 0 {
		return
	}
	seed, err := os.ReadFile(filepath.Join(dir, "seed.sql"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "checks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var checks []seedCheck
	if err := json.Unmarshal(b, &checks); err != nil {
		t.Fatal(err)
	}
	sql := string(seed)
	for i, row := range rows {
		id := literal(fmt.Sprintf("list-%d", i+1))
		sql += fmt.Sprintf(`INSERT INTO "product" ("id", "title", "price", "items") VALUES (%s, 'listed', 1, %s);`+"\n", id, row[0])
		checks = append(checks, seedCheck{
			Name: fmt.Sprintf("the list %s becomes %s", row[0], row[1]),
			SQL:  fmt.Sprintf(`SELECT count(*) = 1 FROM "product" WHERE "id" = %s AND "items" IS %s`, id, row[1]),
		})
	}
	writeFile(t, filepath.Join(dir, "seed.sql"), []byte(sql))
	writeJSON(t, filepath.Join(dir, "checks.json"), checks)
}

// sqliteSeeds seeds rows of SQLite types. Every text column of a row holds
// the same UUID's text, so a foreign key added over a text column
// references a row that exists, as the row's own key. A list is empty: a
// case with listRows seeds lists with elements.
var sqliteSeeds = seeds{
	value: func(col *Column, n, i int) (string, bool) {
		if col.Holds == holdsList {
			return "'[]'", true
		}
		switch sqliteAffinity(col.Type) {
		case sqliteText:
			return literal(fmt.Sprintf("%08x-0000-4000-8000-000000000000", n)), true
		case sqliteInteger, sqliteReal, sqliteNumeric:
			return "7", true
		case sqliteBlob:
			return "x'07'", true
		}
		return "", false
	},
	same: "IS",
}

// TestSQLiteSeeds: every column type of the SQLite fixtures gets a seed.
func TestSQLiteSeeds(t *testing.T) {
	var types []string
	for name, schema := range sqliteModelFixtures(t) {
		for _, table := range sqliteModel(t, name, schema).Tables {
			for _, col := range table.Columns {
				if _, ok := sqliteSeeds.value(col, 1, 0); !ok {
					types = append(types, col.Type)
				}
			}
		}
	}
	sort.Strings(types)
	if len(types) > 0 {
		t.Errorf("no seed for %v", types)
	}
}
