package cli

import (
	"bytes"
	"context"
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

	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/stack/local"
	"github.com/parable-work/superschematic/internal/testpaths"
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
			require.Contains(t, buf.String(), "Built 6 schema services for shop-stack")
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
func devStack(pgPort, storagePort, apiPort int) map[string]string {
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
      values: { postgresPort: %d, storagePort: %d }
      settings:
        - of: { service: { name: shop-api, kind: API } }
          values: { port: %d }
`, pgPort, storagePort, apiPort),
	}
}

// writeRuntimePaths writes the naming file of the schemas root at
// schemasRoot, its [paths] at this repository's runtime modules, relative
// to the repository root the schemas root sits in, as a repository's own
// naming file places them: a generated server module requires each, and
// builds against the checkout. It skips the test when the superscalar
// checkout is missing.
func writeRuntimePaths(t *testing.T, schemasRoot string) {
	t.Helper()
	paths := testpaths.Local(t)
	repoRoot := filepath.Dir(schemasRoot)
	var b strings.Builder
	b.WriteString("[paths]\n")
	for _, p := range []struct{ key, dir string }{
		{"scalar_go", paths.ScalarGo},
		{"schema_ir", paths.SchemaIR},
		{"schema_runtime_go", paths.SchemaRuntimeGo},
		{"http_runtime_go", paths.HTTPRuntimeGo},
	} {
		rel, err := filepath.Rel(repoRoot, p.dir)
		require.NoError(t, err)
		fmt.Fprintf(&b, "%s = %q\n", p.key, filepath.ToSlash(rel))
	}
	require.NoError(t, os.WriteFile(filepath.Join(schemasRoot, "superschematic.toml"), []byte(b.String()), 0o644))
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
// it builds the stack and shop-api with its database, which writes
// shop-api's generated entrypoint and scaffolds its implementation, runs
// Postgres with shop-db migrated and fake-gcs-server with shop-api's bucket
// (D54), builds the entrypoint and starts it with its resolved config, and
// on cancellation, as on Ctrl-C, stops it and, with --remove-data, removes
// the containers. The entrypoint reads its
// whole config at startup, so a server that answers shows its variables
// reached it: it listens on PORT, refuses to start without the secret
// STRIPE_KEY, and answers /readyz 200 only while the database at the URL
// the local target derived answers.
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
	pgPort, storagePort, apiPort := freeTCPPort(t), freeTCPPort(t), freeTCPPort(t)
	writeFiles(t, filepath.Join(servicesRoot, "dev-stack"), devStack(pgPort, storagePort, apiPort))
	writeRuntimePaths(t, filepath.Dir(servicesRoot))
	outputRoot := t.TempDir()
	stateDir, err := local.EnsureStateDir(filepath.Dir(servicesRoot), "dev-stack", "Dev")
	require.NoError(t, err)
	require.NoError(t, local.WriteSecret(filepath.Join(stateDir, local.SecretsFile), "PaymentsSecrets.STRIPE_KEY", "sk_test_dev"))
	container, storage := local.ContainerName("dev-stack", "Dev"), local.StorageContainerName("dev-stack", "Dev")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "--force", "--volumes", container, storage).Run() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buf := &lockedBuffer{}
	root := New(Config{})
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"stack", "dev", filepath.Join(servicesRoot, "dev-stack"), "--out", outputRoot, "--remove-data"})
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	deadline := time.Now().Add(4 * time.Minute)
	for !strings.Contains(buf.String(), "is running:") {
		select {
		case err := <-done:
			t.Fatalf("stack dev returned before the server ran: %v\n%s", err, buf)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("shop-api did not come up:\n%s", buf)
		}
		time.Sleep(100 * time.Millisecond)
	}
	base := local.ServerURL(apiPort)
	for path, want := range map[string]struct {
		status int
		body   string
	}{
		"/healthz":          {http.StatusOK, `"ok"`},
		"/readyz":           {http.StatusOK, `"ready"`},
		"/api/products/p-1": {http.StatusNotImplemented, "Product.GetProduct"},
	} {
		resp, err := http.Get(base + path)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		require.NoError(t, err)
		require.Equal(t, want.status, resp.StatusCode, "%s: %s\n%s", path, body, buf)
		require.Contains(t, string(body), want.body, path)
	}
	require.FileExists(t, filepath.Join(programDir(outputRoot, "dev-stack", "Dev"), local.ModelsDir, "shop-db.json"))
	require.Contains(t, buf.String(), "+ implementation scaffold of shop-api written to")
	require.Contains(t, buf.String(), fmt.Sprintf(`"msg":"listening","stack":"dev-stack","server":"shop-api","addr":":%d"`, apiPort))
	require.Contains(t, buf.String(), "migrate shop-db: ")
	// shop-api's bucket is on the emulator, which the summary names.
	emulator := local.StorageURL(storagePort)
	resp, err := http.Get(emulator + local.StorageReadinessPath + "/shop-media")
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, buf.String())
	require.Contains(t, buf.String(), "bucket   shop-media")

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, buf.String())
	case <-time.After(time.Minute):
		t.Fatalf("stack dev did not stop:\n%s", buf)
	}
	require.Contains(t, buf.String(), "removed container "+container)
	require.Contains(t, buf.String(), "removed container "+storage)
	require.Error(t, exec.Command("docker", "container", "inspect", container).Run(), "the container outlived --remove-data")
	require.Error(t, exec.Command("docker", "container", "inspect", storage).Run(), "the storage emulator outlived --remove-data")
	t.Log(buf.String())
}
