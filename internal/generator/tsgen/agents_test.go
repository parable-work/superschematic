package tsgen

import (
	"testing"
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
	"a run's token is refused by the plain grant authenticator, and the dispatcher's authenticator resolves it as the run, narrowed to whom it acts for",
}

// agentsSQLiteCheck is what tsc checks the package against on SQLite
// (D189): the dispatcher of the ./d1 entry over the ./orm/sqlite entry's
// repositories, its requests claimed over the SqlDatabase.
const agentsSQLiteCheck = `import { createAgents, type Agents, type SqlDatabase, type WorkerQueue } from '@superschematic/http-runtime/d1';
import { createOrm } from './orm-sqlite';
import { agentRunRequestQueue } from './queues';

export function agents(db: SqlDatabase): [Agents, WorkerQueue] {
  const agents = createAgents({ orm: createOrm(db), requests: agentRunRequestQueue, endpoint: 'http://localhost/api/agents' });
  return [agents, agents.workerQueue(db)];
}
`

// agentsRun is the agent dispatcher's end-to-end test (D158) on either
// dialect.
var agentsRun = featureRun{
	fixture:     agentsFixture,
	test:        "agents.test.ts",
	files:       []string{"agent_runner.ts"},
	stubs:       agentsProviders,
	check:       map[string]string{"agents.check.ts": agentsCheck},
	sqliteCheck: map[string]string{"agents.check.ts": agentsSQLiteCheck},
	cases:       agentsCases,
	keep:        "AGENTS_KEEP",
	secrets:     []string{"site-key-4b9e17c2", "vault-broker-token-9d2c", "grant_"},
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
	runFeature(t, agentsRun, false)
}

// TestAgentRunsEndToEndOnSQLite runs the same cases on SQLite (D189): the
// dispatcher over the ./orm/sqlite entry's repositories on openSqlite's
// connection to a file the database's SQLite create.sql made, the runs'
// Lease, Retries and Budget through the core in its transactions, and
// every queue claimed on the file. It needs no database server.
func TestAgentRunsEndToEndOnSQLite(t *testing.T) {
	runFeature(t, agentsRun, true)
}
