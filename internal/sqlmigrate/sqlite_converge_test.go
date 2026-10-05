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
// last version's create.sql. The steps run as the runner runs them: foreign
// keys on, and
// off around a step that asks, with PRAGMA foreign_key_check before its
// commit.
//
// The checks live in testdata/sqliteconverge, a module of its own, so the
// SQLite driver stays out of this module. It needs no database server.
func TestConvergenceOnSQLite(t *testing.T) {
	cases := t.TempDir()
	cascade := false
	for _, pc := range sqlitePlanCases() {
		if writeSQLitePlanCase(t, filepath.Join(cases, pc.golden()), pc) {
			cascade = true
		}
		writeListRows(t, filepath.Join(cases, pc.golden()), pc.lists)
		if plan := pc.plan(t); plan.Expanded != "" {
			from, _ := pc.models(t)
			dir := filepath.Join(cases, "expanded-"+pc.golden())
			writeExpandedCase(t, dir, pc, plan, sqliteCreateSQL(t, from), sqliteSeeds)
			writeListRows(t, dir, pc.lists)
		}
	}
	// A rebuild of a parent must keep the rows of its ON DELETE CASCADE
	// children: some case must check that.
	if !cascade {
		t.Fatal("no case rebuilds a table whose ON DELETE CASCADE child has a seeded row")
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

	module := t.TempDir()
	if err := os.CopyFS(module, os.DirFS(filepath.Join("testdata", "sqliteconverge"))); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "-count=1", "-v", "-run", "^TestConvergence$", "./...")
	cmd.Dir = module
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=readonly", "CGO_ENABLED=0", "SQLITECONVERGE_CASES="+cases)
	out, err := cmd.CombinedOutput()
	t.Logf("sqliteconverge:\n%s", out)
	if err != nil {
		t.Fatalf("sqliteconverge failed: %v", err)
	}
	if !strings.Contains(string(out), "--- PASS: TestConvergence") {
		t.Fatal("sqliteconverge did not run TestConvergence")
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
// writePlanCase does for Postgres. It reports whether the case rebuilds a
// table whose ON DELETE CASCADE child gets a seeded row the checks follow.
func writeSQLitePlanCase(t *testing.T, dir string, pc planCase) bool {
	t.Helper()
	from, to := pc.models(t)
	plan := pc.plan(t)
	writeJSON(t, filepath.Join(dir, "plan.json"), plan)
	writeFile(t, filepath.Join(dir, "to.sql"), []byte(sqliteCreateSQL(t, to)))
	if pc.fromEmpty {
		return false
	}
	writeFile(t, filepath.Join(dir, "from.sql"), []byte(sqliteCreateSQL(t, from)))
	if pc.noSeed != "" {
		t.Logf("%s: no rows seeded: %s", pc.golden(), pc.noSeed)
		return false
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
	for _, table := range to.Tables {
		for _, fk := range table.ForeignKeys {
			if fk.OnDelete == "CASCADE" && rebuilt[fk.RefTable] && tablesByName(from)[r.prevTable(table.Name)] != nil {
				return true
			}
		}
	}
	return false
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
