package tsgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
)

// referencesFixture is the DB service whose tables keep references and
// files (D154): tickets with a conversation, references to rows of any
// table and files; notes with an audience, references to tickets and notes
// alone and files of a few media types.
const referencesFixture = "fixture-references-db"

// referencesCheck is the file tsc checks the fixture's package against:
// the ORM's references and attachments members, typed.
const referencesCheck = `import type { FixtureReferencesDbOrm, FixtureReferencesDbTx } from './orm';
import type { Backlinks, Attachment } from '@superschematic/http-runtime/postgres';

export async function backlinks(orm: FixtureReferencesDbOrm, key: string): Promise<Backlinks> {
  return orm.references.referencedBy({ table: 'Ticket', key });
}

export async function files(tx: FixtureReferencesDbTx, key: string): Promise<Attachment[]> {
  return tx.attachments.list({ table: 'Note', key });
}
`

// referencesCases are the cases references.test.ts runs: D154's "done
// when" in the generated repositories.
var referencesCases = []string{
	"a file posted on one row's thread is filed onto a second row, which lists the first in its backlinks",
	"a ref block in a row's thread is a reference of the row, which the target lists",
	"a caller who cannot read a referenced row sees neither the reference nor a count, and its owner sees a count",
	"a target's delete clears the references to it and tells each row that held one",
	"an upload's bytes are held to what it declared, a row to its bounds, and an object is deleted once no file names it",
}

// TestReferencesTablesEndToEnd generates fixture-references-db's
// TypeScript package, type-checks it against referencesCheck, and runs
// testdata/orm/references.test.ts in it with bun over the generated
// create.sql in a schema of its own at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL: every case of referencesCases
// must pass. It skips without the database.
func TestReferencesTablesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' end-to-end check in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run references and attachments against Postgres")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, referencesFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", referencesFixture, err)
	}
	files := map[string]string{"create.sql": createSQLOf(t, schema, referencesFixture)}
	for _, name := range []string{"harness.ts", "references.test.ts"} {
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
		extra:    map[string]string{"references.check.ts": referencesCheck},
	}})
	dir := filepath.Join(typesRoot, referencesFixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "references.junit.xml")
	run := exec.Command(bunPath, "test", "./references.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = dir
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("the end-to-end test failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range referencesCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(referencesCases) {
		t.Errorf("bun ran %d cases, and referencesCases names %d:\n%s", len(cases), len(referencesCases), junit)
	}
	t.Logf("bun ran the end-to-end test:\n%s", out)
}
