package generator

import (
	_ "embed"
	"os"
	"os/exec"
	"strings"
	"testing"
)

//go:embed testdata/parties_plain_messages_rust.rs
var partiesPlainMessagesRustTest string

// TestRustPartiesPlainMessages builds fixture-parties-plain-messages-db's
// Rust types (D157, D193), whose messages keep references and hold no
// files and whose reports hold files, and runs
// testdata/parties_plain_messages_rust.rs over the runtime's orm::inbound
// on a SQLite file with the build's SQLite DDL applied and, with
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL, on Postgres in a schema of its
// own (#618): a party's text is filed onto a report and onto the granted
// job once, the run request handled the first time and not run again, and
// the two runs' answers are the same.
//
// The test needs cargo, which it skips without, and runs outside -short.
func TestRustPartiesPlainMessages(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Rust inbound path in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the Rust inbound path")
	}
	cases := []string{"a text from a party with a grant is filed onto the granted record once, as references alone, when the messages hold no files"}
	onSQLite, onPostgres := rustRuntimeCrate(t, cargo, "fixture-parties-plain-messages-db", "parties_plain_messages", partiesRustManifest, partiesPlainMessagesRustTest, os.Getenv(behaviorServerDatabaseEnv))
	checkRustRuns(t, "parties_plain_messages", cases, onSQLite, onPostgres)
	for _, out := range []string{onSQLite, onPostgres} {
		if strings.Contains(out, "relay-token") {
			t.Errorf("a relay's token reached the test's output:\n%s", out)
		}
	}
}
