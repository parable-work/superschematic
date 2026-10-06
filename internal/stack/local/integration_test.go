package local_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/stack/local"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
)

// syncBuffer is a buffer the provisioner's output goroutines and the test
// share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// buildMigrateRunner builds superschematic-migrate from runtime/migrate/go.
func buildMigrateRunner(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), local.MigrateBinary)
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/superschematic-migrate")
	cmd.Dir = filepath.Join("..", "..", "..", "runtime", "migrate", "go")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the migration runner: %v\n%s", err, out)
	}
	return bin
}

// copyFakeServer copies the fake server module to dir, with its replace
// directives pointing at this checkout and the HTTP runtime's go.sum, so
// it builds against the runtime's serviceauth package.
func copyFakeServer(t *testing.T, dir string) {
	t.Helper()
	if err := os.CopyFS(dir, os.DirFS(filepath.Join("testdata", "fakeserver"))); err != nil {
		t.Fatal(err)
	}
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	mod = bytes.ReplaceAll(mod, []byte("=> ../../../../../"), []byte("=> "+filepath.ToSlash(repo)+"/"))
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), mod, 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := os.ReadFile(filepath.Join(repo, "runtime", "http", "go", "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), sum, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestLocalStackRuns applies a local environment for real, with Docker: it
// starts a Postgres container of its own, applies a migration to its
// database, builds and runs two fake server modules that honour the
// entrypoint's contract with their resolved environments, waits for
// /readyz, and reads back the variables a server received. The caller
// signs a token with the key its derived field names, and the callee's
// service-auth config verifies it with the HTTP runtime's serviceauth
// package, as a generated server will (D37). Run again, it reuses the
// container, which kept its data, and migrates to the next model. Purge
// removes the container.
func TestLocalStackRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: the local stack runs Docker, Postgres and a server")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("Docker is not available: %v", err)
	}
	runner := buildMigrateRunner(t)

	// A stack of its own, so its container's name is the test's.
	stackName := fmt.Sprintf("local-target-it-%d", os.Getpid())
	pgPort, apiPort, workerPort := freePort(t), freePort(t), freePort(t)
	db := ir.ServiceRef{Name: "ledger-db", Kind: ir.SchemaKindDB}
	api := ir.ServiceRef{Name: "ledger-api", Kind: ir.SchemaKindAPI}
	worker := ir.ServiceRef{Name: "ledger-worker", Kind: ir.SchemaKindAPI}
	greeting := "hello"
	services := []stack.Service{
		{Name: "ledger-db", Kind: ir.SchemaKindDB},
		{
			Name: "ledger-api", Kind: ir.SchemaKindAPI, AuthDB: &db,
			Config: &stack.Config{
				Type: "LedgerConfig",
				Fields: []stack.ConfigField{
					{Name: "GREETING", Required: true, Default: &greeting},
					{Name: "TOKEN", Required: true, Secret: true},
				},
			},
			Operations: []stack.Operation{{Name: "Accounts.getBalance"}},
		},
		{Name: "ledger-worker", Kind: ir.SchemaKindAPI, Calls: []ir.ServiceRef{api}},
	}
	st := &ir.Stack{
		Name:   stackName,
		Deploy: []ir.ServiceRef{api, worker},
		Environments: []*ir.Environment{{
			Name:   "Dev",
			Target: local.Target,
			Values: map[string]any{"postgresPort": float64(pgPort)},
			Settings: []*ir.DeployableSettings{
				{Of: ir.DeployableRef{Service: &api}, Values: map[string]any{"port": float64(apiPort)}},
				{Of: ir.DeployableRef{Service: &worker}, Values: map[string]any{"port": float64(workerPort)}},
			},
		}},
	}
	reg, err := registry.Assemble(registry.DefaultNaming())
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := stack.Resolve(reg, stack.Input{Stack: st, Services: services, Environment: "Dev"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := stack.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	env, err := stack.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	container := local.ContainerName(stackName, "Dev")

	root := t.TempDir()
	outputRoot := filepath.Join(root, "dist")
	for _, server := range []string{"ledger-api", "ledger-worker"} {
		copyFakeServer(t, filepath.Join(outputRoot, local.ModulePath(stackName, server)))
	}
	// The build may add the fake server's own lines to its go.sum.
	t.Setenv("GOFLAGS", "-mod=mod")
	dir := filepath.Join(root, "program")
	writeModel := func(plan string) {
		t.Helper()
		model, err := vectorModel(t, plan, "ledger-db").CanonicalJSON()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, local.ModelsDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, local.ModelsDir, "ledger-db.json"), model, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stateDir, err := local.EnsureStateDir(filepath.Join(root, "schemas"), stackName, "Dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := local.WriteSecret(filepath.Join(stateDir, local.SecretsFile), "LedgerConfig.TOKEN", "s3cret"); err != nil {
		t.Fatal(err)
	}
	// A variable of the test's own environment the server must not see.
	t.Setenv("LEDGER_LEAK", "leaked")

	out := &syncBuffer{}
	prov := &local.Provisioner{Out: out, Migrate: runner, ReadyTimeout: 2 * time.Minute}
	req := registry.ProvisionRequest{Environment: env, Dir: dir, OutputRoot: outputRoot, Backend: local.StateBackend(stateDir)}
	t.Cleanup(func() {
		_ = prov.Purge(context.Background(), req)
		_ = exec.Command("docker", "rm", "--force", "--volumes", container).Run()
		if t.Failed() || testing.Verbose() {
			t.Logf("provisioner output:\n%s", out)
		}
	})
	apply := func() {
		t.Helper()
		if err := prov.Render(env, dir); err != nil {
			t.Fatal(err)
		}
		for _, step := range env.DeployOrder {
			if err := prov.Apply(context.Background(), req, *step); err != nil {
				t.Fatal(err)
			}
		}
	}
	columns := func() string {
		t.Helper()
		out, err := exec.Command("docker", "exec", container, "psql", "--host", "127.0.0.1", "--username", "postgres", "--dbname", "ledger_db",
			"--tuples-only", "--no-align", "--command", "SELECT column_name FROM information_schema.columns WHERE table_name = 'account' ORDER BY ordinal_position").CombinedOutput()
		if err != nil {
			t.Fatalf("read the account table: %v\n%s", err, out)
		}
		return strings.Join(strings.Fields(string(out)), ",")
	}

	writeModel("01-create.plan.json")
	apply()
	if got := columns(); got != "id,name,balance" {
		t.Errorf("account columns after the first run = %s, want id,name,balance", got)
	}

	resp, err := http.Get(local.ServerURL(apiPort) + "/env")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	err = json.NewDecoder(resp.Body).Decode(&got)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"PORT":                   fmt.Sprint(apiPort),
		"GREETING":               "hello",
		"TOKEN":                  "s3cret",
		"LEDGER_DB_DATABASE_URL": local.DatabaseURL(pgPort, "ledger_db"),
	} {
		if got[name] != want {
			t.Errorf("the server got %s=%q, want %q", name, got[name], want)
		}
	}
	if _, leaked := got["LEDGER_LEAK"]; leaked {
		t.Error("the server inherited a variable its environment does not bind")
	}
	if !strings.Contains(out.String(), "[ledger-api] fake server listening on 127.0.0.1:") {
		t.Errorf("output lacks the server's prefixed line:\n%s", out)
	}

	// The worker signs a token with its edge's key, and ledger-api's
	// service-auth config admits it as the worker; a request with no
	// credential has no caller.
	resp, err = http.Get(local.ServerURL(workerPort) + "/call/LEDGER_API_SERVICE")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var caller struct {
		Deployable string   `json:"deployable"`
		Serves     []string `json:"serves"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &caller) != nil || caller.Deployable != "ledger-worker" || strings.Join(caller.Serves, ",") != "ledger-worker" {
		t.Errorf("the worker's call to ledger-api answered %d %s, want 200 and the caller ledger-worker", resp.StatusCode, body)
	}
	resp, err = http.Get(local.ServerURL(apiPort) + "/whoami")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a call to ledger-api with no credential answered %d, want 401", resp.StatusCode)
	}

	// Destroy stops the server and the container, which keeps its data;
	// the next run starts it again and migrates to the next model.
	if err := prov.Destroy(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := http.Get(local.ServerURL(apiPort) + "/readyz"); err == nil {
		t.Error("the server still answers after Destroy")
	}
	writeModel("02-add-column-and-index.plan.json")
	apply()
	if got := columns(); got != "id,name,balance,email" {
		t.Errorf("account columns after the second run = %s, want id,name,balance,email", got)
	}

	if err := prov.Purge(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("docker", "container", "inspect", container).Run(); err == nil {
		t.Errorf("container %s is still there after Purge", container)
	}
}
