package tsgen

import (
	"testing"
)

// partiesPlainMessagesRun is the inbound path's filing over messages that
// hold no files (D157): fixture-parties-plain-messages-db's messages keep
// references and no @attachments, and its reports hold files. A party's
// text is filed onto a report and the granted job once, its run request
// handled the first time and not run again.
var partiesPlainMessagesRun = featureRun{
	fixture:     "fixture-parties-plain-messages-db",
	test:        "parties-plain-messages.test.ts",
	stubs:       map[string]string{"sms-relay-a.ts": notificationsProviders["sms-relay-a.ts"]},
	check:       map[string]string{"parties.check.ts": partiesCheck},
	sqliteCheck: map[string]string{"parties.check.ts": partiesSQLiteCheck},
	cases: []string{
		"a text from a party with a grant is filed onto the granted record once, as references alone, when the messages hold no files",
	},
	keep:    "PARTIES_KEEP",
	secrets: []string{"relay-token"},
}

// TestPartiesPlainMessagesEndToEnd runs partiesPlainMessagesRun on
// Postgres, in a schema of its own at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL; it skips without the database.
func TestPartiesPlainMessagesEndToEnd(t *testing.T) {
	runFeature(t, partiesPlainMessagesRun, false)
}

// TestPartiesPlainMessagesEndToEndOnSQLite runs it on a SQLite file the
// database's SQLite create.sql made (D189).
func TestPartiesPlainMessagesEndToEndOnSQLite(t *testing.T) {
	runFeature(t, partiesPlainMessagesRun, true)
}
