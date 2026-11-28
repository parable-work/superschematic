package generator

import (
	_ "embed"
	"testing"
)

//go:embed testdata/parties_plain_messages_go.go
var partiesPlainMessagesGoTest string

// TestGoPartiesPlainMessages holds the Go inbound path's filing to
// fixture-parties-plain-messages-db (D157, D192): its messages keep
// references and hold no files, and its reports hold files. A party's text
// is filed onto a report and onto the granted job once, the run request
// handled the first time and not run again, on a SQLite file and, with the
// Postgres behaviorServerDatabaseEnv names, on Postgres too, the two
// answering alike.
func TestGoPartiesPlainMessages(t *testing.T) {
	runOnSQLite(t, sqliteGoRun{
		db: "fixture-parties-plain-messages-db", name: "parties_plain_messages", source: partiesPlainMessagesGoTest, test: "TestPartiesPlainMessages", env: "PARTIES_TEST_DATABASE_URL",
		cases: []string{
			"a text from a party with a grant is filed onto the granted record once, as references alone, when the messages hold no files",
		},
	})
}
