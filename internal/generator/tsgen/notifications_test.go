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

// notificationsFixture is the DB service whose tickets raise events and
// which holds the notifier's tables and channel identities (D153).
const notificationsFixture = "fixture-notifications-db"

// notificationsCheck is the file tsc checks the fixture's package against:
// the notifier over its ORM and dispatch queue, and the definitions that
// carry each table's events and notifier part.
const notificationsCheck = `import { createNotifier, type Notifier } from '@superschematic/http-runtime/postgres';
import { createOrm, ticketTable, noticeTable } from './orm';
import { noticeWorkQueue } from './queues';

export function notifier(pool: Parameters<typeof createOrm>[0]): Notifier {
  return createNotifier({ orm: createOrm(pool), dispatch: noticeWorkQueue });
}
export const kinds: string[] = (ticketTable.events ?? []).map(e => e.kind);
export const part: 'inbox' | undefined = noticeTable.notifier === 'inbox' ? 'inbox' : undefined;
`

// notificationsProviders are the stub channel adapters of
// examples/runtime-providers (D159) the end-to-end test sends through,
// copied beside it.
var notificationsProviders = map[string]string{
	"sms-relay-a.ts": "../../../examples/runtime-providers/providers/sms-relay-a/src/index.ts",
	"mail-relay.ts":  "../../../examples/runtime-providers/providers/mail-relay/src/index.ts",
}

// notificationsCases are the cases notifications.test.ts runs: D153's
// "done when" in the generated repositories.
var notificationsCases = []string{
	"an urgent event reaches two recipients by SMS, skips one who cannot read the record, and escalates to email when not acknowledged in time",
	"a reply of ack acknowledges the notice and stops its escalation, and undo takes it back",
	"a reply of done 142 changes the record as its sender, and the audit names the channel and what read the text",
	"a low-trust channel is refused an operation its trust does not allow, and an address entered by hand only acknowledges",
	"a reply names its notice by reference unless exactly one waits on its sender",
	"an address is verified by the code sent to it and replied with, and an unknown sender reaches nothing",
	"a failed send falls through to the next channel, and an adapter that is down is skipped and alerted on the others",
	"quiet hours hold a normal notice, digests batch, and record sends nothing",
	"a delivery's statuses move it on and a cleared reference is recorded on its holder",
	"password reset sends a code through the email adapter and the code sets the password",
}

// TestNotificationsEndToEnd generates fixture-notifications-db's
// TypeScript package, type-checks it against notificationsCheck, and runs
// testdata/orm/notifications.test.ts in it with bun over the generated
// create.sql in a schema of its own at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, with the runtime-providers
// example's SMS and mail stubs as the channels: every case of
// notificationsCases must pass. It skips without the database.
func TestNotificationsEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' end-to-end check in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the notifier against Postgres")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, notificationsFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", notificationsFixture, err)
	}
	files := map[string]string{"create.sql": createSQLOf(t, schema, notificationsFixture)}
	for _, name := range []string{"harness.ts", "notifications.test.ts"} {
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
		name:     notificationsFixture,
		schema:   schema,
		registry: reg,
		extra:    map[string]string{"notifications.check.ts": notificationsCheck},
	}})
	dir := filepath.Join(typesRoot, notificationsFixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if keep := os.Getenv("NOTIFY_KEEP"); keep != "" {
		_ = exec.Command("cp", "-R", dir, keep).Run()
	}
	report := filepath.Join(dir, "notifications.junit.xml")
	run := exec.Command(bunPath, "test", "./notifications.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
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
	for _, name := range notificationsCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(notificationsCases) {
		t.Errorf("bun ran %d cases, and notificationsCases names %d:\n%s", len(cases), len(notificationsCases), junit)
	}
	if strings.Contains(string(out), "relay-token") {
		t.Errorf("a relay's token reached the test's output:\n%s", out)
	}
	t.Logf("bun ran the end-to-end test:\n%s", out)
}
