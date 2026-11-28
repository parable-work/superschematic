package generator

import (
	_ "embed"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

//go:embed testdata/agents_rust.rs
var agentsRustTest string

// agentsRustCases are the cases testdata/agents_rust.rs runs: D158's and
// D183's "done when" in the Rust repositories and runtime (D193), the
// TypeScript repositories' cases (tsgen's agentsCases).
var agentsRustCases = []string{
	"a run a schedule's deadline starts reads only its target and the rows its role reaches from it",
	"an agent run's MCP search returns only rows within its scope",
	"each turn replays the run's earlier completions with their tool calls and the provider's state unchanged, and a reader gets the state redacted",
	"an ask operation waits for an approval given on any channel, then runs",
	"the same operation with a changed amount asks again",
	"hitting a budget cap pauses the agent, and the total cap stops scheduled runs and asks before a principal's",
	"pausing all agents cancels a run in flight and returns its assigned row to the queue",
	"the transcript holds no credential value",
	"an inbound message from a party starts an agent run that acts for it and obeys nothing it says, and a send that needs approval waits on the policy",
	"a party's run reaches only the paths its grant reaches, and an entered sender's run reads and asks before every write whatever the policy says",
	"an agent asks in the row's thread and makes a follow-up row when nobody answers by the deadline",
	"a sandbox that fails is started again by Retries, and the run holds its Lease while it runs",
	"administering agents is permissions, and an agent cannot start a run",
}

// TestRustAgentRunsEndToEnd builds fixture-agent-runs-db's Rust types
// (D158, D183, D193) and runs testdata/agents_rust.rs over its generated
// repositories and the runtime's orm::agents, orm::inbound and orm::notify
// on Postgres, in a schema of its own at
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL with the database's create.sql
// applied: the agent routes on a local port, each run's instance the
// runtime's run_agent_instance in a task of the test's process, which the
// runtime-providers example's model stub completes the turns of and whose
// credential the example's vault broker resolves. Every case of
// agentsRustCases must run, and no credential value or run token may reach
// the test's output.
//
// The cases run on a SQLite file with the build's SQLite DDL applied
// (#618) and, with the database, on Postgres too, and their answers must
// be the same.
//
// The test needs cargo, which it skips without, and runs outside -short.
func TestRustAgentRunsEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Rust agent dispatcher in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the Rust agent dispatcher")
	}
	providers, err := filepath.Abs("../../examples/runtime-providers/rust")
	if err != nil {
		t.Fatal(err)
	}
	manifest := strings.NewReplacer(
		"{{stub-model}}", strconv.Quote(filepath.ToSlash(filepath.Join(providers, "stub-model"))),
		"{{vault-broker}}", strconv.Quote(filepath.ToSlash(filepath.Join(providers, "vault-broker"))),
	).Replace(agentsRustManifest)
	onSQLite, onPostgres := rustRuntimeCrate(t, cargo, "fixture-agent-runs-db", "agents", manifest, agentsRustTest, os.Getenv(behaviorServerDatabaseEnv))
	checkRustRuns(t, "agents", agentsRustCases, onSQLite, onPostgres)
	for _, out := range []string{onSQLite, onPostgres} {
		for _, secret := range []string{"site-key-4b9e17c2", "vault-broker-token-9d2c", "grant_"} {
			if strings.Contains(out, secret) {
				t.Errorf("%q reached the test's output:\n%s", secret, out)
			}
		}
	}
}

// agentsRustManifest is the test crate's Cargo.toml, given the types
// crate's name and directory and the HTTP runtime's directory, with the
// model stub's and the vault broker's directories in place of their marks.
const agentsRustManifest = `# fixture-agent-runs-db's Rust repositories and agent dispatcher end to
# end (D158, D183, D193).
[package]
name = "agents-rust"
version = "0.0.0"
edition = "2021"
publish = false

[dev-dependencies]
async-trait = "0.1.89"
axum = "0.8.9"
reqwest = { version = "0.13.2", default-features = false }
serde_json = "1.0.149"
tokio = { version = "1.52.2", features = ["macros", "net", "rt-multi-thread", "time"] }
types = { package = %q, path = %q, features = ["postgres", "sqlite"] }
runtime = { package = "superschematic-http-runtime", path = %q, features = ["postgres", "sqlite"] }
stub-model = { path = {{stub-model}} }
vault-broker = { path = {{vault-broker}} }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`
