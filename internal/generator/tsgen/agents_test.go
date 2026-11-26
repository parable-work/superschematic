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

// agentsFixture is the DB service of the agent orchestration (D158): tasks
// with their notes and invoices, a customer's grants, the notifier, and
// the orchestration's tables.
const agentsFixture = "fixture-agent-runs-db"

// agentsCheck is the file tsc checks the fixture's package against: the
// orchestration over its ORM and request queue, and the definitions that
// carry each table's part.
const agentsCheck = `import { createAgents, type Agents } from '@superschematic/http-runtime/postgres';
import { createOrm, agentTable, agentRunTable } from './orm';
import { agentRunRequestQueue } from './queues';

export function agents(pool: Parameters<typeof createOrm>[0]): Agents {
  return createAgents({ orm: createOrm(pool), requests: agentRunRequestQueue, endpoint: 'http://localhost/api/agents' });
}
export const part: 'definitions' | undefined = agentTable.agents === 'definitions' ? 'definitions' : undefined;
export const runs: 'runs' | undefined = agentRunTable.agents === 'runs' ? 'runs' : undefined;
`

// agentsProviders are the stubs of examples/runtime-providers (D159) the
// end-to-end test runs its agents with, copied beside it: the local-process
// sandbox, the model stub, the vault broker a run's credentials resolve
// through, and the SMS relay an approval is asked for on.
var agentsProviders = map[string]string{
	"local-sandbox.ts": "../../../examples/runtime-providers/providers/local-sandbox/src/index.ts",
	"stub-model.ts":    "../../../examples/runtime-providers/providers/stub-model/src/index.ts",
	"vault-broker.ts":  "../../../examples/runtime-providers/providers/vault-broker/src/index.ts",
	"sms-relay-a.ts":   "../../../examples/runtime-providers/providers/sms-relay-a/src/index.ts",
}

// agentsCases are the cases agents.test.ts runs: D158's "done when" in the
// generated repositories, with section 5's check of an agent run's search.
var agentsCases = []string{
	"a run a schedule's deadline starts reads only its target and the rows its role reaches from it",
	"an agent run's MCP search returns only rows within its scope",
	"an ask operation waits for an approval given on any channel, then runs",
	"the same operation with a changed amount asks again",
	"hitting a budget cap pauses the agent, and the total cap stops scheduled runs and asks before a principal's",
	"pausing all agents cancels a run in flight and returns its assigned row to the queue",
	"the transcript holds no credential value",
	"an inbound message from a party starts an agent run that acts for it and obeys nothing it says, and a send that needs approval waits on the policy",
	"an agent asks in the row's thread and makes a follow-up row when nobody answers by the deadline",
	"a sandbox that fails is started again by Retries, and the run holds its Lease while it runs",
	"administering agents is permissions, and an agent cannot start a run",
}

// TestAgentRunsEndToEnd generates fixture-agent-runs-db's TypeScript
// package, type-checks it against agentsCheck, and runs
// testdata/orm/agents.test.ts in it with bun over the generated create.sql
// in a schema of its own at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL: the
// dispatcher starts each run's instance with the runtime-providers
// example's local-process sandbox, which runs testdata/orm/agent_runner.ts
// under bun, whose turns the example's model stub completes. Every case of
// agentsCases must pass, and no credential value or run token may reach
// the test's output. It skips without the database.
func TestAgentRunsEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' end-to-end check in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the agent orchestration against Postgres")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, agentsFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", agentsFixture, err)
	}
	files := map[string]string{"create.sql": createSQLOf(t, schema, agentsFixture)}
	for _, name := range []string{"harness.ts", "agents.test.ts", "agent_runner.ts"} {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	for name, path := range agentsProviders {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{
		name:     agentsFixture,
		schema:   schema,
		registry: reg,
		extra:    map[string]string{"agents.check.ts": agentsCheck},
	}})
	dir := filepath.Join(typesRoot, agentsFixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if keep := os.Getenv("AGENTS_KEEP"); keep != "" {
		_ = exec.Command("cp", "-R", dir, keep).Run()
	}
	report := filepath.Join(dir, "agents.junit.xml")
	run := exec.Command(bunPath, "test", "./agents.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = dir
	run.Env = append(os.Environ(), "AGENT_RUNNER_BUN="+bunPath)
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
	for _, name := range agentsCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(agentsCases) {
		t.Errorf("bun ran %d cases, and agentsCases names %d:\n%s", len(cases), len(agentsCases), junit)
	}
	for _, secret := range []string{"site-key-4b9e17c2", "vault-broker-token-9d2c", "grant_"} {
		if strings.Contains(string(out), secret) {
			t.Errorf("%q reached the test's output:\n%s", secret, out)
		}
	}
	t.Logf("bun ran the end-to-end test:\n%s", out)
}
