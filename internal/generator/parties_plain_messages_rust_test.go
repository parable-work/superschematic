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
// on Postgres, in a schema of its own at
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL: a party's text is filed onto a
// report and onto the granted job once, the run request handled the first
// time and not run again. Rust serves the inbound path on Postgres alone.
//
// The test needs cargo, which it skips without, the database, and runs
// outside -short.
func TestRustPartiesPlainMessages(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Rust inbound path in -short mode")
	}
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the Rust inbound path against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the Rust inbound path")
	}
	out := rustRuntimeCrate(t, cargo, "fixture-parties-plain-messages-db", "parties_plain_messages", partiesRustManifest, partiesPlainMessagesRustTest, "PARTIES_TEST_DATABASE_URL="+dsn)
	t.Logf("tests/parties_plain_messages.rs:\n%s", out)
	if name := "a text from a party with a grant is filed onto the granted record once, as references alone, when the messages hold no files"; !strings.Contains(out, "case: "+name+"\n") {
		t.Errorf("the case %q did not run:\n%s", name, out)
	}
	if strings.Contains(out, "relay-token") {
		t.Errorf("a relay's token reached the test's output:\n%s", out)
	}
}
