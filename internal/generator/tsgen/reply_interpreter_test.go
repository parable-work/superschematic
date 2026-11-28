package tsgen

import (
	"testing"
)

// replyInterpreterProviders are the stubs of examples/runtime-providers
// (D159) the reply interpreter's test runs with: the local-process sandbox,
// the model stub, the vault broker agent_runner.ts links, and the SMS relay
// the replies come in and go back on.
var replyInterpreterProviders = map[string]string{
	"local-sandbox.ts": "../../../examples/runtime-providers/providers/local-sandbox/src/index.ts",
	"stub-model.ts":    "../../../examples/runtime-providers/providers/stub-model/src/index.ts",
	"vault-broker.ts":  "../../../examples/runtime-providers/providers/vault-broker/src/index.ts",
	"sms-relay-a.ts":   "../../../examples/runtime-providers/providers/sms-relay-a/src/index.ts",
}

// replyInterpreterCases are the cases reply_interpreter.test.ts runs: a
// reply interpreter other than keywords (#602, D158).
var replyInterpreterCases = []string{
	"a reply no keyword reads is read by an agent run that acts for its sender, and its action is repeated back with an undo",
	"keywords stay the default, and an identity entered by hand is no model's to read",
	"the run holds its sender's permissions, and its scope does not widen",
}

// replyInterpreterRun is the reply interpreter's end-to-end test on either
// dialect, over the agent orchestration's fixture, which holds the
// notifier's tables beside the orchestration's.
var replyInterpreterRun = featureRun{
	fixture:     agentsFixture,
	test:        "reply_interpreter.test.ts",
	files:       []string{"agent_runner.ts"},
	stubs:       replyInterpreterProviders,
	check:       map[string]string{"agents.check.ts": agentsCheck},
	sqliteCheck: map[string]string{"agents.check.ts": agentsSQLiteCheck},
	cases:       replyInterpreterCases,
	keep:        "REPLIES_KEEP",
	secrets:     []string{"relay-token", "grant_"},
}

// TestReplyInterpreterEndToEnd generates fixture-agent-runs-db's
// TypeScript package and runs testdata/orm/reply_interpreter.test.ts in it
// with bun on Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL: a reply
// that no keyword reads goes to an agent run, whose instance runs
// agent_runner.ts under the local-process sandbox and whose turns the model
// stub completes. It skips without the database.
func TestReplyInterpreterEndToEnd(t *testing.T) {
	runFeature(t, replyInterpreterRun, false)
}

// TestReplyInterpreterEndToEndOnSQLite runs the same cases on SQLite
// (D189). It needs no database server.
func TestReplyInterpreterEndToEndOnSQLite(t *testing.T) {
	runFeature(t, replyInterpreterRun, true)
}
