package generator

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/testpaths"
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
// types (D153, D191) and runs testdata/notifications_rust.rs over its
// generated repositories and the runtime's orm::notify on Postgres, in a
// schema of its own at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL with the
// database's create.sql applied, with a stand-in SMS relay and mail relay
// as the channel adapters: the test's drain hands each due message of the
// dispatch queue to Notifier::handle as its worker would, and every case of
// notificationsRustCases must run. The relay's token never reaches the
// test's output.
//
// The test needs cargo, which it skips without, the database, and runs
// outside -short.
func TestRustNotificationsEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Rust notifier in -short mode")
	}
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the Rust notifier against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the Rust notifier")
	}
	const db = "fixture-notifications-db"
	paths := testpaths.Local(t)
	outputRoot := testpaths.TempDir(t)
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, db))
	if err != nil {
		t.Fatalf("load %s: %v", db, err)
	}
	types, _ := cfg.Outputs["types"].(map[string]any)
	if types == nil {
		types = map[string]any{}
		cfg.Outputs["types"] = types
	}
	types["rust"] = map[string]any{"enabled": true}
	// The Go ORM, which a DB service always generates, imports its Go
	// types.
	types["go"] = map[string]any{"enabled": true}
	rustOnPostgresAlone(cfg)
	if _, err := Run(schema, cfg, Options{
		OutputRoot:       outputRoot,
		Paths:            paths,
		Naming:           naming.Default(),
		Clock:            codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		DependencyConfig: func(string) (*schemaconfig.SchemaConfig, bool) { return nil, false },
	}); err != nil {
		t.Fatalf("run %s: %v", db, err)
	}

	typesDir := TypesDir(outputRoot, "rust", db)
	crateDir := filepath.Join(outputRoot, "notifications-rust")
	for _, dir := range []string{"src", "tests"} {
		if err := os.MkdirAll(filepath.Join(crateDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, db), "create.sql"), filepath.Join(crateDir, "create.sql"))
	lock, err := os.ReadFile(filepath.Join(paths.HTTPRuntimeRust, "Cargo.lock"))
	if err != nil {
		t.Fatalf("the HTTP runtime's lockfile: %v", err)
	}
	for name, content := range map[string]string{
		"Cargo.toml":             fmt.Sprintf(notificationsRustManifest, naming.Default().RustTypesCrate(db), filepath.ToSlash(typesDir), filepath.ToSlash(paths.HTTPRuntimeRust)),
		"Cargo.lock":             string(lock),
		"src/lib.rs":             "//! fixture-notifications-db's Rust repositories and notifier: see tests/.\n",
		"tests/notifications.rs": notificationsRustTest,
	} {
		if err := os.WriteFile(filepath.Join(crateDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(outputRoot, "target")
	}
	var out []byte
	for _, args := range [][]string{
		{"clippy", "--quiet", "--all-targets", "--", "-D", "warnings"},
		{"test", "--quiet", "--", "--nocapture"},
	} {
		cmd := exec.Command(cargo, args...)
		cmd.Dir = crateDir
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target, "NOTIFY_TEST_DATABASE_URL="+dsn)
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cargo %s in %s: %v\n%s", args[0], crateDir, err, out)
		}
	}
	t.Logf("tests/notifications.rs:\n%s", out)
	for _, name := range notificationsRustCases {
		if !strings.Contains(string(out), "case: "+name+"\n") {
			t.Errorf("the case %q did not run:\n%s", name, out)
		}
	}
	if strings.Contains(string(out), "relay-token") {
		t.Errorf("a relay's token reached the test's output:\n%s", out)
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
types = { package = %q, path = %q, features = ["postgres"] }
runtime = { package = "superschematic-http-runtime", path = %q, features = ["postgres", "identity-postgres"] }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`
