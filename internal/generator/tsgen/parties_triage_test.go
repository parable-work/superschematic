package tsgen

import (
	"testing"
)

// partiesTriageProviders are the stub channel adapters of
// examples/runtime-providers (D159) the triage test runs with: the SMS
// relay, and the chat bridge whose loop relays a stand-in network's
// messages to the notifier's routes.
var partiesTriageProviders = map[string]string{
	"sms-relay-a.ts": "../../../examples/runtime-providers/providers/sms-relay-a/src/index.ts",
	"chat-bridge.ts": "../../../examples/runtime-providers/providers/chat-bridge/src/index.ts",
}

// partiesTriageCases are the cases parties_triage.test.ts runs: settling a
// held message onto a party, and a messaging bridge's inbound (#606).
var partiesTriageCases = []string{
	"a held message settled onto a party by an address entered by hand is filed as from the party, at the lower trust",
	"a held message settled onto a party to be verified waits for the code its address answers, then is filed",
	"settling is held to the triage permission, the party's row and an address that is no user's",
	"a messaging bridge relays a party's message and an unknown sender's down the inbound path, with its relay key alone",
}

// partiesTriageRun is the triage test on either dialect, over the parties
// fixture.
var partiesTriageRun = featureRun{
	fixture:     partiesFixture,
	test:        "parties_triage.test.ts",
	stubs:       partiesTriageProviders,
	check:       map[string]string{"parties.check.ts": partiesCheck},
	sqliteCheck: map[string]string{"parties.check.ts": partiesSQLiteCheck},
	cases:       partiesTriageCases,
	keep:        "TRIAGE_KEEP",
	secrets:     []string{"relay-token", "chat-relay-key-5e1b"},
}

// TestPartiesTriageEndToEnd generates fixture-parties-db's TypeScript
// package and runs testdata/orm/parties_triage.test.ts in it with bun on
// Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL. It skips without
// the database.
func TestPartiesTriageEndToEnd(t *testing.T) {
	runFeature(t, partiesTriageRun, false)
}

// TestPartiesTriageEndToEndOnSQLite runs the same cases on SQLite (D189).
// It needs no database server.
func TestPartiesTriageEndToEndOnSQLite(t *testing.T) {
	runFeature(t, partiesTriageRun, true)
}
