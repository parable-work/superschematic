package tsgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
)

// audienceQueueFixture is the DB service whose pages inherit their
// folder's audience through a queue (D156, #734).
const audienceQueueFixture = "fixture-audience-queue-db"

// audienceQueueCheck is the file tsc checks the fixture's package
// against: the page table's definition carries the statement
// resolveAudience runs, and the folder's none.
const audienceQueueCheck = `import { folderTable, pageTable } from './orm';

export const resolves: string = pageTable.audience!.resolve!;
export const folderResolves: string | undefined = folderTable.audience!.resolve;
`

// audienceQueueCases are the cases audience_queue.test.ts runs on
// Postgres, as audienceQueueORMCases are the Go ORM's (ormgen) and
// audience_queue_rules.rs the Rust repositories'.
var audienceQueueCases = []string{
	"a folder's change of audience commits without its pages, enqueued once on the folder's queue",
	"a page read right after the worker drains the queue shows the folder's new audience",
}

// TestAudienceQueueEndToEnd holds the generated repositories to an
// audience resolved through a queue (#734). It loads
// fixture-audience-queue-db, generates its TypeScript package,
// type-checks it against audienceQueueCheck, and runs
// testdata/orm/audience_queue.test.ts in it with bun over the generated
// create.sql in a schema of its own at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL: a folder's change of audience
// enqueues it once and leaves its pages as they were, and a page read
// right after resolveAudience drains the queue shows the folder's new
// audience. Every case must pass when the database is set; without it
// the package is still generated and type-checked.
func TestAudienceQueueEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' audience queue in -short mode")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, audienceQueueFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", audienceQueueFixture, err)
	}
	files := map[string]string{"create.sql": createSQLOf(t, schema, audienceQueueFixture)}
	if !strings.Contains(files["create.sql"], "INSERT INTO audience_resolving_queue") {
		t.Fatalf("create.sql's cascade enqueues nothing on the folder's queue")
	}
	for _, name := range []string{"harness.ts", "audience_queue.test.ts"} {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{
		name:     audienceQueueFixture,
		schema:   schema,
		registry: reg,
		extra:    map[string]string{"audience_queue.check.ts": audienceQueueCheck},
	}})
	dir := filepath.Join(typesRoot, audienceQueueFixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "audience_queue.junit.xml")
	run := exec.Command(bunPath, "test", "./audience_queue.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
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
	for _, name := range audienceQueueCases {
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
	if len(cases) != len(audienceQueueCases) {
		t.Fatalf("bun ran %d cases of audience_queue.test.ts, and the test names %d:\n%s", len(cases), len(audienceQueueCases), junit)
	}
	if !postgres {
		t.Logf("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL is not set: the cases were skipped")
	}
}
