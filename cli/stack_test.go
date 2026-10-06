package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/stack/local"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// TestTheStackGroupHoldsDev: the core binary has the stack group, and
// registerStackCommands's constructors are its subcommands.
func TestTheStackGroupHoldsDev(t *testing.T) {
	root := New(Config{})
	cmd, _, err := root.Find([]string{"stack", "dev"})
	require.NoError(t, err)
	require.Equal(t, "dev", cmd.Name())
	require.Equal(t, "stack", cmd.Parent().Name())
	require.Len(t, cmd.Parent().Commands(), len(stackCommands))
}

// TestStackDevNeedsALocalEnvironment: stack dev builds the stack, then
// refuses a stack with no environment on the local target, and an
// environment on another target.
func TestStackDevNeedsALocalEnvironment(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "stack shop-stack has no environment on the local target"},
		{[]string{"--environment", "Staging"}, "environment Staging of stack shop-stack is on target fake; stack dev runs an environment on the local target"},
		{[]string{"--environment", "Nightly"}, "stack shop-stack has no environment Nightly"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			servicesRoot := prepareStackServicesRoot(t)
			buf := new(bytes.Buffer)
			root := New(Config{}, &stacktest.Extension{})
			root.SetOut(buf)
			root.SetErr(buf)
			root.SetArgs(append([]string{"stack", "dev", filepath.Join(servicesRoot, "shop-stack"), "--out", t.TempDir()}, tc.args...))
			err := root.Execute()
			require.Error(t, err, buf.String())
			require.Contains(t, err.Error(), tc.want)
			require.Contains(t, buf.String(), "Built 4 schema services for shop-stack")
		})
	}
}

// TestStackDevRefusesAServiceThatIsNoStack names an API service.
func TestStackDevRefusesAServiceThatIsNoStack(t *testing.T) {
	servicesRoot := prepareStackServicesRoot(t)
	root := New(Config{})
	root.SetOut(new(bytes.Buffer))
	root.SetArgs([]string{"stack", "dev", filepath.Join(servicesRoot, "shop-api")})
	err := root.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "shop-api is a API service; the stack commands take a Stack service")
}

// devStack is a stack over the fixture's shop-api, in the YAML form, whose
// Dev environment runs on the local target on the ports given.
func devStack(pgPort, apiPort int) map[string]string {
	return map[string]string{
		"schema.config.yaml": "name: dev-stack\nkind: Stack\noutputs: {}\n",
		"src/stack.schema.yaml": fmt.Sprintf(`kind: Stack
types:
  Shop:
    name: Shop
    role: EmbeddedStruct
    stack:
      deploy:
        - { name: shop-api, kind: API }
  Dev:
    name: Dev
    role: EmbeddedStruct
    environment:
      target: local
      values: { postgresPort: %d }
      settings:
        - of: { service: { name: shop-api, kind: API } }
          values: { port: %d }
`, pgPort, apiPort),
	}
}

// fakeEntrypoint is a server module that honours the generated
// entrypoint's contract (docs/stack-model.md, section 8.1): it listens on
// $PORT, answers /readyz and echoes its environment at /env.
var fakeEntrypoint = map[string]string{
	"go.mod": "module example.com/fakeentrypoint\n\ngo 1.26\n",
	"main.go": `package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

func main() {
	http.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {})
	http.HandleFunc("GET /env", func(w http.ResponseWriter, _ *http.Request) {
		env := map[string]string{}
		for _, kv := range os.Environ() {
			key, value, _ := strings.Cut(kv, "=")
			env[key] = value
		}
		_ = json.NewEncoder(w).Encode(env)
	})
	println("entrypoint up")
	if err := http.ListenAndServe("127.0.0.1:"+os.Getenv("PORT"), nil); err != nil {
		os.Exit(1)
	}
}
`,
}

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// lockedBuffer is a buffer the command's output goroutines and the test
// share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestStackDevRunsALocalEnvironment runs `stack dev` for real, with Docker:
// it builds the stack and shop-api with its database, runs Postgres with
// shop-db migrated, starts shop-api's entrypoint module with its resolved
// config, and on cancellation, as on Ctrl-C, stops it and, with
// --remove-database, removes the container. The entrypoint generator is
// not here, so the test puts a module that honours its contract where the
// build would write it.
func TestStackDevRunsALocalEnvironment(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: stack dev runs Docker, Postgres and a server")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("Docker is not available: %v", err)
	}
	runner := filepath.Join(t.TempDir(), local.MigrateBinary)
	build := exec.Command("go", "build", "-o", runner, "./cmd/superschematic-migrate")
	build.Dir = filepath.Join("..", "runtime", "migrate", "go")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))
	t.Setenv(local.MigrateEnv, runner)

	servicesRoot := prepareStackServicesRoot(t)
	pgPort, apiPort := freeTCPPort(t), freeTCPPort(t)
	writeFiles(t, filepath.Join(servicesRoot, "dev-stack"), devStack(pgPort, apiPort))
	outputRoot := t.TempDir()
	writeFiles(t, filepath.Join(outputRoot, "server", "dev-stack", "shop-api"), fakeEntrypoint)
	stateDir, err := local.EnsureStateDir(filepath.Dir(servicesRoot), "dev-stack", "Dev")
	require.NoError(t, err)
	require.NoError(t, local.WriteSecret(filepath.Join(stateDir, local.SecretsFile), "PaymentsSecrets.STRIPE_KEY", "sk_test_dev"))
	container := local.ContainerName("dev-stack", "Dev")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "--force", "--volumes", container).Run() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buf := &lockedBuffer{}
	root := New(Config{})
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"stack", "dev", filepath.Join(servicesRoot, "dev-stack"), "--out", outputRoot, "--remove-database"})
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	var env map[string]string
	deadline := time.Now().Add(4 * time.Minute)
	for env == nil {
		select {
		case err := <-done:
			t.Fatalf("stack dev returned before the server ran: %v\n%s", err, buf)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("shop-api did not come up:\n%s", buf)
		}
		if strings.Contains(buf.String(), "is running:") {
			resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/env", apiPort))
			require.NoError(t, err)
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&env))
			_ = resp.Body.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.Equal(t, local.DatabaseURL(pgPort, "shop_db"), env["SHOP_DB_DATABASE_URL"], buf.String())
	require.Equal(t, "sk_test_dev", env["STRIPE_KEY"])
	require.Equal(t, "info", env["LOG_LEVEL"])
	require.Equal(t, fmt.Sprint(apiPort), env["PORT"])
	require.FileExists(t, filepath.Join(programDir(outputRoot, "dev-stack", "Dev"), local.ModelsDir, "shop-db.json"))
	require.Contains(t, buf.String(), "[shop-api] entrypoint up")
	require.Contains(t, buf.String(), "migrate shop-db: ")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, buf.String())
	case <-time.After(time.Minute):
		t.Fatalf("stack dev did not stop:\n%s", buf)
	}
	require.Contains(t, buf.String(), "removed container "+container)
	require.Error(t, exec.Command("docker", "container", "inspect", container).Run(), "the container outlived --remove-database")
	t.Log(buf.String())
}
