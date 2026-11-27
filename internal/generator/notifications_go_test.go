package generator

import (
	_ "embed"
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

//go:embed testdata/notifications_go.go
var notificationsGoTest string

// notificationsGoCases are the cases testdata/notifications_go.go runs:
// D153's "done when" in the Go repositories and the HTTP runtime's notify
// package (D190), the TypeScript repositories' cases (tsgen's
// notificationsCases), and an approval decided by reply, as the Rust ones'
// (D191).
var notificationsGoCases = []string{
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

// TestGoNotificationsEndToEnd generates fixture-notifications-db's Go
// types and ORM (D153, D190), writes testdata/notifications_go.go into the
// ORM's module, which requires the HTTP runtime for its notify, providers
// and identity packages, and runs it, after go vet, over the ORM's
// Notifications on the Postgres behaviorServerDatabaseEnv names, in a
// schema of its own with the database's create.sql applied, with a
// stand-in SMS relay and mail relay as the channel adapters: every case of
// notificationsGoCases must pass, and the relay's token never reaches the
// test's output. It skips without the database and in -short mode.
func TestGoNotificationsEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Go notifier in -short mode")
	}
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the Go notifier against Postgres")
	}
	const db = "fixture-notifications-db"
	paths := testpaths.Local(t)
	outputRoot := t.TempDir()
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, db))
	if err != nil {
		t.Fatalf("load %s: %v", db, err)
	}
	types, _ := cfg.Outputs["types"].(map[string]any)
	if types == nil {
		types = map[string]any{}
		cfg.Outputs["types"] = types
	}
	types["go"] = map[string]any{"enabled": true}
	if _, err := Run(schema, cfg, Options{
		OutputRoot:       outputRoot,
		Paths:            paths,
		Naming:           naming.Default(),
		Clock:            codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		DependencyConfig: func(string) (*schemaconfig.SchemaConfig, bool) { return nil, false },
	}); err != nil {
		t.Fatalf("run %s: %v", db, err)
	}
	ormDir := ORMDir(outputRoot, db)
	if err := os.MkdirAll(filepath.Join(ormDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, db), "create.sql"), filepath.Join(ormDir, "testdata", "create.sql"))
	if err := os.WriteFile(filepath.Join(ormDir, "notifications_test.go"), []byte(notificationsGoTest), 0o644); err != nil {
		t.Fatal(err)
	}
	httpRuntime := naming.Default().HTTPRuntimeGoModule
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t), "NOTIFY_TEST_DATABASE_URL=" + dsn}
	var out []byte
	for _, args := range [][]string{
		{"mod", "edit", "-require=" + httpRuntime + "@v0.0.0-00010101000000-000000000000", "-replace=" + httpRuntime + "=" + paths.HTTPRuntimeGo},
		{"mod", "tidy"},
		{"vet", "./..."},
		{"test", "-count=1", "-v", "-run", "TestNotifications", "./..."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = ormDir
		cmd.Env = append(cmd.Environ(), env...)
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the generated ORM module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	t.Logf("generated ORM tests:\n%s", out)
	for _, name := range notificationsGoCases {
		if want := "--- PASS: TestNotifications/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}
	if strings.Contains(string(out), "relay-token") {
		t.Errorf("a relay's token reached the test's output:\n%s", out)
	}
}
