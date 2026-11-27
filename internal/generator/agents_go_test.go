package generator

import (
	_ "embed"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

//go:embed testdata/agents_go.go
var agentsGoTest string

// agentsGoCases are the cases testdata/agents_go.go runs: D158's and
// D183's "done when" in the Go repositories and the HTTP runtime's agents
// package (D192), the TypeScript repositories' cases (tsgen's agentsCases)
// and the Rust ones' (D193).
var agentsGoCases = []string{
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
	"a run's token on an app's own route acts as the run, its reach narrowed and every call held to its autonomy",
	"administering agents is permissions, and an agent cannot start a run",
}

func sortedModuleKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestGoAgentRunsEndToEnd generates fixture-agent-runs-db's Go types and
// ORM (D158, D183, D192), writes testdata/agents_go.go into the ORM's
// module, which requires the HTTP runtime for its agents, inbound, notify
// and providers packages and the runtime-providers example's stub model
// and vault broker, and runs it, after go vet, over the ORM's
// Orchestration on the Postgres behaviorServerDatabaseEnv names, in a
// schema of its own with the database's create.sql applied: the agent
// routes on a local port, each run's instance the runtime's
// RunAgentInstance in a goroutine of the test's process. Every case of
// agentsGoCases must pass, and no credential value or run token may reach
// the test's output. It skips without the database and in -short mode.
func TestGoAgentRunsEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Go agent dispatcher in -short mode")
	}
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the Go agent dispatcher against Postgres")
	}
	providers, err := filepath.Abs("../../examples/runtime-providers/providers")
	if err != nil {
		t.Fatal(err)
	}
	modules := map[string]string{
		"example.com/runtime-providers/stub-model":   filepath.Join(providers, "stub-model"),
		"example.com/runtime-providers/vault-broker": filepath.Join(providers, "vault-broker"),
	}
	out := goRuntimeModule(t, "fixture-agent-runs-db", "agents", agentsGoTest, "TestAgentRuns", modules, "AGENTS_TEST_DATABASE_URL="+dsn)
	t.Logf("generated ORM tests:\n%s", out)
	for _, name := range agentsGoCases {
		if want := "--- PASS: TestAgentRuns/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(out, want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}
	for _, secret := range []string{"site-key-4b9e17c2", "vault-broker-token-9d2c"} {
		if strings.Contains(out, secret) {
			t.Errorf("%q reached the test's output", secret)
		}
	}
	// A run's token is grant_ and its random part; the cases' names hold
	// grant_ followed by a word.
	if runToken.MatchString(out) {
		t.Errorf("a run's token reached the test's output")
	}
}

var runToken = regexp.MustCompile(`grant_[A-Za-z0-9_-]{16,}`)
