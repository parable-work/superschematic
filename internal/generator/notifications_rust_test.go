package generator

import (
	_ "embed"
	"os"
	"os/exec"
	"strings"
	"testing"
)

//go:embed testdata/notifications_rust.rs
var notificationsRustTest string

// notificationsRustCases are the cases testdata/notifications_rust.rs
// runs: D153's "done when" in the Rust repositories and runtime (D191),
// the TypeScript repositories' cases (tsgen's notificationsCases), and an
// approval decided by reply.
var notificationsRustCases = []string{
	"an urgent event reaches two recipients by SMS, skips one who cannot read the record, and escalates to email when not acknowledged in time",
	"a reply of ack acknowledges the notice and stops its escalation, and undo takes it back",
	"a reply of done 142 changes the record as its sender, and the audit names the channel and what read the text",
	"a low-trust channel is refused an operation its trust does not allow, and an address entered by hand only acknowledges",
	"a reply names its notice by reference unless exactly one waits on its sender",
	"an address is verified by the code sent to it and replied with, and an unknown sender reaches nothing",
	"a failed send falls through to the next channel, and an adapter that is down is skipped and alerted on the others",
	"quiet hours hold a normal notice, digests batch, and record sends nothing",
	"a delivery's statuses move it on and a cleared reference is recorded on its holder",
	"password reset sends a code through the email adapter and the code sets the password",
	"an approval is asked of the principals who hold its permission, and approve and deny by reply decide it within the channel's trust",
}

// TestRustNotificationsEndToEnd builds fixture-notifications-db's Rust
// types (D153, D191, #618) and runs testdata/notifications_rust.rs over
// its generated repositories and the runtime's orm::notify on a SQLite file
// with the build's SQLite DDL applied and, with
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL, on Postgres in a schema of its
// own with create.sql applied, with a stand-in SMS relay and mail relay as
// the channel adapters: the test's drain hands each due message of the
// dispatch queue to Notifier::handle as its worker would. Every case of
// notificationsRustCases must run on each, and their answers must be the
// same. The relay's token never reaches the test's output.
//
// The test needs cargo, which it skips without, and runs outside -short.
func TestRustNotificationsEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Rust notifier in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the Rust notifier")
	}
	onSQLite, onPostgres := rustRuntimeCrate(t, cargo, "fixture-notifications-db", "notifications", notificationsRustManifest, notificationsRustTest, os.Getenv(behaviorServerDatabaseEnv))
	checkRustRuns(t, "notifications", notificationsRustCases, onSQLite, onPostgres)
	for _, out := range []string{onSQLite, onPostgres} {
		if strings.Contains(out, "relay-token") {
			t.Errorf("a relay's token reached the test's output:\n%s", out)
		}
	}
}

// notificationsRustManifest is the test crate's Cargo.toml, given the
// types crate's name and directory and the HTTP runtime's directory.
const notificationsRustManifest = `# fixture-notifications-db's Rust repositories and notifier end to end
# (D153, D191).
[package]
name = "notifications-rust"
version = "0.0.0"
edition = "2021"
publish = false

[dev-dependencies]
async-trait = "0.1.89"
serde_json = "1.0.149"
tokio = { version = "1.52.2", features = ["macros", "rt-multi-thread", "time"] }
types = { package = %q, path = %q, features = ["postgres", "sqlite"] }
runtime = { package = "superschematic-http-runtime", path = %q, features = ["postgres", "sqlite", "identity-postgres", "identity-sqlite"] }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`
