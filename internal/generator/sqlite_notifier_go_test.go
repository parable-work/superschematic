package generator

import (
	_ "embed"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

//go:embed testdata/sqlite_notifier_go.go
var sqliteNotifierGoTest string

//go:embed testdata/sqlite_parties_go.go
var sqlitePartiesGoTest string

//go:embed testdata/sqlite_agents_go.go
var sqliteAgentsGoTest string

// sqliteGoRun is a generated module's test on SQLite of the notifier, the
// parties or the agents (#618): its fixture, the script, the test it runs,
// the variable that names the Postgres it runs on too, and the cases that
// must pass.
type sqliteGoRun struct {
	db, name, source, test, env string
	modules                     map[string]string
	cases                       []string
}

// runOnSQLite runs r's script in the generated ORM module of r.db over a
// SQLite file the build's SQLite DDL creates, and, with the Postgres
// behaviorServerDatabaseEnv names, the same cases there too: every case
// must pass on SQLite, and on Postgres too with the two answering alike.
func runOnSQLite(t *testing.T, r sqliteGoRun) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping the generated ORM's build in -short mode")
	}
	var env []string
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn != "" {
		env = append(env, r.env+"="+dsn)
	}
	out := goRuntimeModule(t, r.db, r.name, r.source, "^"+r.test+"$", r.modules, env...)
	t.Logf("generated ORM tests:\n%s", out)
	for _, name := range r.cases {
		if want := "--- PASS: " + r.test + "/sqlite/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(out, want) {
			t.Errorf("the generated module's test did not pass %q on SQLite", name)
		}
	}
	if dsn != "" {
		alike := regexp.MustCompile(`SQLite and Postgres answered \d+ cases alike`).FindString(out)
		if alike == "" || !strings.Contains(out, "--- PASS: "+r.test+"/postgres ") {
			t.Errorf("the cases did not pass alike on Postgres")
		}
	}
	if strings.Contains(out, "relay-token") {
		t.Errorf("a relay's token reached the test's output")
	}
	return out
}

// TestGoNotifierOnSQLite holds the Go notifier to fixture-notifications-db
// on a SQLite file (#618, as D189): the ORM's SQLite database's
// Notifications runs the notify package's statements in SQLite's words and
// claims its dispatch queue on SQLite, and the cases answer as on Postgres.
func TestGoNotifierOnSQLite(t *testing.T) {
	runOnSQLite(t, sqliteGoRun{
		db: "fixture-notifications-db", name: "notifier_sqlite", source: sqliteNotifierGoTest, test: "TestNotifierOnSQLite", env: "NOTIFY_TEST_DATABASE_URL",
		cases: []string{
			"an urgent event reaches two recipients, skips one who cannot read the record, and escalates to a second channel when not acknowledged",
			"a reply of ack acknowledges the notice and stops its escalation, and a reply of done changes the record as its sender",
			"the dispatch queue's worker claims its messages over the database and drains them",
		},
	})
}

// TestGoAgentRunsOnSQLite holds the Go agent dispatcher to
// fixture-agent-runs-db on a SQLite file (#618, as D189): the ORM's SQLite
// database's Orchestration runs the agents package's statements in
// SQLite's words, a run's Lease, Retries and status operations through the
// behaviors' core in the SQLite repositories' transactions, and claims the
// dispatcher's queue on SQLite, with each run's instance the runtime's
// RunAgentInstance in a goroutine of the test, and the cases answer as on
// Postgres. No credential value or run token reaches the output.
func TestGoAgentRunsOnSQLite(t *testing.T) {
	providers, err := filepath.Abs("../../examples/runtime-providers/providers")
	if err != nil {
		t.Fatal(err)
	}
	out := runOnSQLite(t, sqliteGoRun{
		db: "fixture-agent-runs-db", name: "agents_sqlite", source: sqliteAgentsGoTest, test: "TestAgentRunsOnSQLite", env: "AGENTS_TEST_DATABASE_URL",
		modules: map[string]string{
			"example.com/runtime-providers/stub-model":   filepath.Join(providers, "stub-model"),
			"example.com/runtime-providers/vault-broker": filepath.Join(providers, "vault-broker"),
		},
		cases: []string{
			"a run a schedule's deadline starts reads only its target and the rows its role reaches from it",
			"an ask operation waits for an approval given on any channel, then runs",
			"hitting a budget cap pauses the agent, and the total cap stops scheduled runs and asks before a principal's",
		},
	})
	for _, secret := range []string{"site-key-4b9e17c2", "vault-broker-token-9d2c"} {
		if strings.Contains(out, secret) {
			t.Errorf("%q reached the test's output", secret)
		}
	}
	if runToken.MatchString(out) {
		t.Errorf("a run's token reached the test's output")
	}
}

// TestGoPartiesOnSQLite holds the Go inbound path to fixture-parties-db
// on a SQLite file (#618, as D189): the ORM's SQLite database's
// Orchestration runs the inbound package's statements in SQLite's words,
// files a message's photo through its attachments there, and claims the
// @runRequests queue on SQLite, and the cases answer as on Postgres.
func TestGoPartiesOnSQLite(t *testing.T) {
	runOnSQLite(t, sqliteGoRun{
		db: "fixture-parties-db", name: "parties_sqlite", source: sqlitePartiesGoTest, test: "TestPartiesOnSQLite", env: "PARTIES_TEST_DATABASE_URL",
		cases: []string{
			"a text with a photo from a party with a grant becomes a row on the granted record, with the photo attached",
			"a text from an unknown number lands in triage",
			"the same text from a party whose grant has ended reaches nothing",
		},
	})
}
