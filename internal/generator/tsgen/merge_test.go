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

// mergeFixture is the DB service whose Label and Customer implement
// Mergeable (D147), with the tables that reference them.
const mergeFixture = "fixture-merge-db"

// mergeCheck is the file tsc checks the fixture's repositories against: a
// table that implements Mergeable has mergeInto, which takes the two keys
// and answers the rows it repointed and deleted by table, and a table that
// does not has none.
const mergeCheck = `import type { MergeResult } from '@superschematic/http-runtime/postgres';
import type { FixtureMergeDbOrm, LabelRow } from './orm';
import { labelTable, documentTable } from './orm';

export async function fold(orm: FixtureMergeDbOrm, from: LabelRow['id'], into: LabelRow['id']): Promise<MergeResult> {
  const result = await orm.label.mergeInto({ from, into });
  const repointed: number | undefined = result.repointed.DocumentLabel;
  return repointed === undefined ? result : orm.customer.mergeInto({ from, into });
}

// @ts-expect-error A table that does not implement Mergeable has no mergeInto.
export const none = (orm: FixtureMergeDbOrm) => orm.document.mergeInto;
export const steps: readonly string[] = labelTable.merge!.steps.map(step => step.source + '.' + step.field);
export const plain: undefined = documentTable.merge as undefined;
`

// mergeCases are the cases merge.test.ts runs on Postgres, by name without
// the dialect's suffix.
var mergeCases = []string{
	"a merge repoints every relation that references the row and deletes it softly",
	"a row a unique index would refuse is deleted, and the rest repointed",
	"on a soft-deletable table the refused row is stamped deleted where the index holds live rows alone",
	"a unique index over an expression that reads the relation's column finds its collisions",
	"a table that deletes for good loses the row merged away, through the core",
	"a merge of a row into itself, of a row that is gone, and into a row that references it is refused and writes nothing",
	"a merge in a transaction rolls back with it",
}

// TestMergeIntoOnPostgres holds the merge the generator writes into a
// Mergeable table's definition to the HTTP runtime's mergeInto (D147). It
// loads fixture-merge-db through the TypeScript reader, generates its
// TypeScript package with the core's registry, type-checks it against
// mergeCheck, and runs testdata/orm/merge.test.ts in it with bun over the
// generated create.sql in a schema of its own at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL. Every case must pass when the
// database is set; without it the package is still generated and
// type-checked. It skips without bun unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS
// says the gate must run.
func TestMergeIntoOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' merge end-to-end check in -short mode")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, mergeFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", mergeFixture, err)
	}
	files := map[string]string{"create.sql": createSQLOf(t, schema, mergeFixture)}
	for _, name := range []string{"harness.ts", "merge.test.ts"} {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{
		name:     mergeFixture,
		schema:   schema,
		registry: reg,
		extra:    map[string]string{"merge.check.ts": mergeCheck},
	}})
	dir := filepath.Join(typesRoot, mergeFixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "merge.junit.xml")
	run := exec.Command(bunPath, "test", "./merge.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
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
	postgres := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != ""
	for _, name := range mergeCases {
		name += ", on Postgres"
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Fatalf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil:
			t.Fatalf("%q failed:\n%s", name, junit)
		case tc.Skipped != nil && postgres:
			t.Fatalf("%q was skipped:\n%s", name, junit)
		}
	}
	if len(cases) != len(mergeCases) {
		t.Fatalf("bun ran %d cases of merge.test.ts, and the test names %d:\n%s", len(cases), len(mergeCases), junit)
	}
	if !postgres {
		t.Logf("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL is not set: the cases were skipped")
	}
}
