package tsgen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// referencesSQLiteCheck is the file tsc checks the fixture's package
// against on SQLite (D182): the ./orm/sqlite entry's createOrm opens the
// ORM's references and attachments members over a SqlDatabase.
const referencesSQLiteCheck = `import type { SqlDatabase, Backlinks, Attachment } from '@superschematic/http-runtime/d1';
import type { FixtureReferencesDbTx } from './orm';
import { createOrm } from './orm-sqlite';

export async function backlinks(db: SqlDatabase, key: string): Promise<Backlinks> {
  return createOrm(db).references.referencedBy({ table: 'Ticket', key });
}

export async function files(tx: FixtureReferencesDbTx, key: string): Promise<Attachment[]> {
  return tx.attachments.list({ table: 'Note', key });
}
`

// referencesSQLiteCases are the cases references_sqlite.test.ts runs:
// D154's "done when" in the generated repositories on SQLite, and a
// table that composes behaviors written through the core (D186).
var referencesSQLiteCases = []string{
	"a file of one row is filed onto a second row, which lists the first in its backlinks",
	"a ref block in a row's thread is a reference of the row, which the target lists",
	"a caller who cannot read a referenced row sees neither the reference nor a count, and its owner sees a count",
	"a target's delete clears the references to it and tells each row that held one",
	"an upload's bytes are held to what it declared, a row to its bounds, and an object is deleted once no file names it",
	"a table that composes behaviors is written through the core, and a raw write of its rows is read",
}

// TestReferencesTablesEndToEndOnSQLite generates fixture-references-db's
// TypeScript package with its ./orm/sqlite entry, type-checks it against
// referencesSQLiteCheck, writes the database's sqlite/create.sql beside it
// and runs testdata/orm/references_sqlite.test.ts with bun, over
// openSqlite's connection to a file it makes (D182): every case of
// referencesSQLiteCases must pass. It needs no database server.
func TestReferencesTablesEndToEndOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' end-to-end check on SQLite in -short mode")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, referencesFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", referencesFixture, err)
	}
	model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: referencesFixture}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("build %s's SQLite model: %v", referencesFixture, err)
	}
	createSQL, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatalf("write %s's SQLite create.sql: %v", referencesFixture, err)
	}
	files := map[string]string{"sqlite/create.sql": createSQL}
	for _, name := range []string{"harness.ts", "references_sqlite.test.ts"} {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{
		name:     referencesFixture,
		schema:   schema,
		registry: reg,
		sqlite:   true,
		extra:    map[string]string{"references-sqlite.check.ts": referencesSQLiteCheck},
	}})
	dir := filepath.Join(typesRoot, referencesFixture)
	for name, text := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "references-sqlite.junit.xml")
	run := exec.Command(bunPath, "test", "./references_sqlite.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = dir
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("the end-to-end test on SQLite failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range referencesSQLiteCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(referencesSQLiteCases) {
		t.Errorf("bun ran %d cases, and referencesSQLiteCases names %d:\n%s", len(cases), len(referencesSQLiteCases), junit)
	}
	t.Logf("bun ran the end-to-end test on SQLite:\n%s", out)
}

// d1Fixture is the runtime's workerd fixture that runs
// fixture-references-db's repositories over D1 (D182).
const d1Fixture = "runtime/http/typescript/test/workerd/fixtures/d1-orm"

// TestTheWorkerdD1FixtureIsCurrent: the workerd fixture d1-orm runs
// fixture-references-db's generated orm-sqlite.ts and its sqlite/create.sql
// as this build writes them (D182): records-orm.ts is the file, its import
// of the runtime's ./d1 entry made the fixture's built dist/d1.js, and
// records-sql.ts the script. -update rewrites both; scripts/workerd-test.mjs
// runs the fixture under the pinned workerd.
func TestTheWorkerdD1FixtureIsCurrent(t *testing.T) {
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, referencesFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", referencesFixture, err)
	}
	output, err := Generate(schema, Options{SchemaName: referencesFixture, Registry: reg, SQLite: true})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	orm, err := os.ReadFile(filepath.Join(dir, ORMSQLiteFile))
	if err != nil {
		t.Fatal(err)
	}
	const runtime = "'@superschematic/http-runtime/d1'"
	if !strings.Contains(string(orm), runtime) {
		t.Fatalf("%s imports no %s", ORMSQLiteFile, runtime)
	}
	header := "// Written by TestTheWorkerdD1FixtureIsCurrent (internal/generator/tsgen) from\n// fixture-references-db's orm-sqlite.ts, its runtime the built dist/d1.js.\n"
	records := header + strings.ReplaceAll(string(orm), runtime, "'../../../../dist/d1.js'")
	model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: referencesFixture}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	script, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	quoted, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	sql := "// Written by TestTheWorkerdD1FixtureIsCurrent (internal/generator/tsgen) from\n// fixture-references-db's sqlite/create.sql.\nexport const createSQL = " + string(quoted) + ";\n"
	root := filepath.Join(testpaths.RepoRoot(t), d1Fixture)
	for name, want := range map[string]string{"records-orm.ts": records, "records-sql.ts": sql} {
		path := filepath.Join(root, name)
		if *update {
			if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Errorf("%s/%s is not what this build writes (run with -update to accept)", d1Fixture, name)
		}
	}
}
