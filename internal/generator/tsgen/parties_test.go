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

// partiesFixture is the DB service whose contacts are parties, which holds
// the conversations, the run requests queue, the grants a party reaches
// jobs through, and the notifier's tables a user's reply runs through
// (D157).
const partiesFixture = "fixture-parties-db"

// partiesCheck is the file tsc checks the fixture's package against: the
// inbound path over its ORM, notifier and run requests queue, and the
// definitions that mark the party table and the conversations.
const partiesCheck = `import { createInbound, createNotifier, type Inbound, type RunRequest } from '@superschematic/http-runtime/postgres';
import { createOrm, contactTable, messageTable } from './orm';
import { noticeWorkQueue, runRequestQueue } from './queues';

export function inbound(pool: Parameters<typeof createOrm>[0]): Inbound {
  const orm = createOrm(pool);
  return createInbound({ orm, notifier: createNotifier({ orm, dispatch: noticeWorkQueue }), runs: runRequestQueue });
}
export const title: string | undefined = contactTable.party?.title;
export const part: 'messages' | undefined = messageTable.conversations === 'messages' ? 'messages' : undefined;
export const shape = (r: RunRequest): [string, string, string, string] => [r.trigger, r.party.table, r.message, r.grant];
`

// partiesCases are the cases parties.test.ts runs: D157's "done when" and
// the path's other turns, in the generated repositories.
var partiesCases = []string{
	"a text with a photo from a party with a grant becomes a row on the granted record, with the photo attached",
	"a text from an unknown number lands in triage",
	"the same text from a party whose grant has ended reaches nothing, until its identity is verified again",
	"email failing DMARC is treated as unknown, and an authenticated email threads to its party",
	"a party with no grant is on its timeline and in triage, and a user's text is the notifier's reply",
	"two parties sharing an address are told apart by the thread, and held in triage otherwise",
	"sending to a party takes the permission, keeps to the rate limit, waits for approval when asked, and stops after STOP",
}

// TestPartiesEndToEnd generates fixture-parties-db's TypeScript package,
// type-checks it against partiesCheck, and runs
// testdata/orm/parties.test.ts in it with bun over the generated create.sql
// in a schema of its own at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, with
// the runtime-providers example's SMS and mail relays as the stub
// adapters: every case of partiesCases must pass. It skips without the
// database.
func TestPartiesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' end-to-end check in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the inbound path against Postgres")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, partiesFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", partiesFixture, err)
	}
	files := map[string]string{"create.sql": createSQLOf(t, schema, partiesFixture)}
	for _, name := range []string{"harness.ts", "parties.test.ts"} {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	for name, path := range notificationsProviders {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{
		name:     partiesFixture,
		schema:   schema,
		registry: reg,
		extra:    map[string]string{"parties.check.ts": partiesCheck},
	}})
	dir := filepath.Join(typesRoot, partiesFixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if keep := os.Getenv("PARTIES_KEEP"); keep != "" {
		_ = exec.Command("cp", "-R", dir, keep).Run()
	}
	report := filepath.Join(dir, "parties.junit.xml")
	run := exec.Command(bunPath, "test", "./parties.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
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
	for _, name := range partiesCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(partiesCases) {
		t.Errorf("bun ran %d cases, and partiesCases names %d:\n%s", len(cases), len(partiesCases), junit)
	}
	if strings.Contains(string(out), "relay-token") {
		t.Errorf("a relay's token reached the test's output:\n%s", out)
	}
	t.Logf("bun ran the end-to-end test:\n%s", out)
}
