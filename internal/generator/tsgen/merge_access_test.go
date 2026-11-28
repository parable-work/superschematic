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

// mergeAccessFixture is the DB service whose tables keep to per-record
// access (D156) and merge (D147): labels with an audience, the notes and
// boards that reference them, and brands whose name is Aliased.
const mergeAccessFixture = "fixture-merge-access-db"

// mergeAccessCheck is the file tsc checks the fixture's package against:
// each step of a label's merge says the rows it writes as per-record
// access holds them, and the layout marks the row Aliased's resolve
// answers.
const mergeAccessCheck = `import { labelTable, fixtureMergeAccessDbRules } from './orm';

export const reach: readonly (string | undefined)[] = labelTable.merge!.steps.map(step => step.reach?.type);
export const answers: string | undefined = fixtureMergeAccessDbRules.types.Brand.uses[0]!.operations.find(op => op.name === 'resolve')!.answersRow;
`

// mergeAccessCases are the cases merge_access.test.ts runs on Postgres, as
// mergeAccessORMCases are the Go ORM's (ormgen) and merge_access_rules.rs
// the Rust repositories'.
var mergeAccessCases = []string{
	"a user who may not write one of the two labels merges neither into the other, and writes nothing",
	"a user who may write both labels, and every row that references the one merged away, merges one into the other",
	"a user who may not write a note on the label merged away merges neither, naming the notes, and writes nothing",
	"a user who may not write a board that holds the label merged away merges neither, naming the boards, and writes nothing",
	"a merge outside any operation repoints the rows no caller is held to",
	"a party merges two labels only when its grants reach the notes on the one merged away",
	"resolve answers a caller nothing of a brand it may not read",
	"a name another brand holds is refused naming nothing of that brand",
	"a user who may write both brands merges one into the other, its aliases and names with it",
}

// TestMergeAccessEndToEnd holds the generated repositories' mergeInto, and
// Aliased's resolve and refusals, to per-record access (D147, D156). It
// loads fixture-merge-access-db through the TypeScript reader, generates
// its TypeScript package with the core's registry, type-checks it against
// mergeAccessCheck, and runs testdata/orm/merge_access.test.ts in it with
// bun over the generated create.sql in a schema of its own at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL: a merge of a row its caller may
// not write, or into one, is not found; one that would repoint or delete a
// note or a board's pair its caller may not write is MergeError
// out_of_reach, naming the table, and writes nothing; a merge outside any
// operation repoints them; resolve answers no row for a brand its caller
// does not see; and a name another brand holds is refused naming nothing of
// it. Every case must pass when the database is set; without it the
// package is still generated and type-checked. It skips without bun unless
// SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must run.
func TestMergeAccessEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' merge under per-record access in -short mode")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, mergeAccessFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", mergeAccessFixture, err)
	}
	files := map[string]string{"create.sql": createSQLOf(t, schema, mergeAccessFixture)}
	for _, name := range []string{"harness.ts", "merge_access.test.ts"} {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{
		name:     mergeAccessFixture,
		schema:   schema,
		registry: reg,
		extra:    map[string]string{"merge_access.check.ts": mergeAccessCheck},
	}})
	dir := filepath.Join(typesRoot, mergeAccessFixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "merge_access.junit.xml")
	run := exec.Command(bunPath, "test", "./merge_access.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
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
	if !postgres {
		delete(cases, "(unnamed)")
	}
	for _, name := range mergeAccessCases {
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
	if len(cases) != len(mergeAccessCases) {
		t.Fatalf("bun ran %d cases of merge_access.test.ts, and the test names %d:\n%s", len(cases), len(mergeAccessCases), junit)
	}
	if !postgres {
		t.Logf("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL is not set: the cases were skipped")
	}
}
