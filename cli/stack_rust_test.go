package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/stack/local"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// rustFixtures holds the servergen Rust fixture (D57), whose rs-stock and
// rs-pricing the stack below runs.
const rustFixtures = "../internal/generator/servergen/testdata/rust/services"

// rustDevStack deploys rs-stock, a Rust server over rs-db, its authDb,
// and rs-pricing, the Rust server it calls, on the local target, each on
// a port of its own.
func rustDevStack(pgPort, stockPort, pricingPort int) map[string]string {
	return map[string]string{
		"schema.config.yaml": "name: rs-dev-stack\nkind: Stack\noutputs: {}\n",
		"src/stack.schema.yaml": fmt.Sprintf(`kind: Stack
types:
  Stock:
    name: Stock
    role: EmbeddedStruct
    stack:
      deploy:
        - { name: rs-stock, kind: API }
        - { name: rs-pricing, kind: API }
  Dev:
    name: Dev
    role: EmbeddedStruct
    environment:
      target: local
      values: { postgresPort: %d }
      settings:
        - of: { service: { name: rs-stock, kind: API } }
          values: { port: %d }
        - of: { service: { name: rs-pricing, kind: API } }
          values: { port: %d }
`, pgPort, stockPort, pricingPort),
	}
}

// writeRustRuntimePaths writes the naming file of the schemas root at
// schemasRoot as writeRuntimePaths does, with this repository's Rust
// runtimes and superscalar's Rust crate beside the Go modules.
func writeRustRuntimePaths(t *testing.T, schemasRoot string) {
	t.Helper()
	writeRuntimePaths(t, schemasRoot)
	paths := testpaths.Local(t)
	repoRoot := filepath.Dir(schemasRoot)
	naming := filepath.Join(schemasRoot, "superschematic.toml")
	data, err := os.ReadFile(naming)
	require.NoError(t, err)
	var b strings.Builder
	b.Write(data)
	for _, p := range []struct{ key, dir string }{
		{"scalar_rust", paths.ScalarRust},
		{"schema_runtime_rust", paths.SchemaRuntimeRust},
		{"http_runtime_rust", paths.HTTPRuntimeRust},
	} {
		rel, err := filepath.Rel(repoRoot, p.dir)
		require.NoError(t, err)
		fmt.Fprintf(&b, "%s = %q\n", p.key, filepath.ToSlash(rel))
	}
	require.NoError(t, os.WriteFile(naming, []byte(b.String()), 0o644))
}

// TestStackDevRunsARustServer runs `stack dev` for real, with Docker and
// cargo (D57): it builds the stack and its services, which writes each
// Rust server's crate into the output root's Cargo workspace and its
// lockfile, and scaffolds each implementation; runs Postgres with rs-db
// migrated; builds each crate with cargo, frozen to the lockfile, and
// starts rs-pricing, then rs-stock, which calls it, each waited on until
// /readyz answers. rs-stock answers on the port its setting gives it, its
// scaffold answers 501, and an end user registers and signs in with the
// identity runtime over the migrated user model.
func TestStackDevRunsARustServer(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: stack dev runs Docker, Postgres and cargo")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("Docker is not available: %v", err)
	}
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo is not available")
	}
	runner := filepath.Join(t.TempDir(), local.MigrateBinary)
	build := exec.Command("go", "build", "-o", runner, "./cmd/superschematic-migrate")
	build.Dir = filepath.Join("..", "runtime", "migrate", "go")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))
	t.Setenv(local.MigrateEnv, runner)

	servicesRoot := filepath.Join(testpaths.TempDir(t), "schemas", "services")
	for _, service := range []string{"rs-db", "rs-pricing", "rs-stock"} {
		copyDir(t, filepath.Join(rustFixtures, service), filepath.Join(servicesRoot, service))
	}
	pgPort, stockPort, pricingPort := freeTCPPort(t), freeTCPPort(t), freeTCPPort(t)
	writeFiles(t, filepath.Join(servicesRoot, "rs-dev-stack"), rustDevStack(pgPort, stockPort, pricingPort))
	writeRustRuntimePaths(t, filepath.Dir(servicesRoot))
	// Cargo resolves a crate's path dependencies from its directory's
	// physical path, so the output root is one with no link above it, as
	// the generated crates' relative paths expect.
	outputRoot := testpaths.TempDir(t)
	container := local.ContainerName("rs-dev-stack", "Dev")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "--force", "--volumes", container).Run() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buf := &lockedBuffer{}
	root := New(Config{})
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"stack", "dev", filepath.Join(servicesRoot, "rs-dev-stack"), "--out", outputRoot, "--remove-data"})
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()

	deadline := time.Now().Add(10 * time.Minute)
	for !strings.Contains(buf.String(), "is running:") {
		select {
		case err := <-done:
			t.Fatalf("stack dev returned before the servers ran: %v\n%s", err, buf)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Rust servers did not come up:\n%s", buf)
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.FileExists(t, filepath.Join(outputRoot, "Cargo.lock"), buf.String())
	for _, server := range []string{"rs-pricing", "rs-stock"} {
		require.Contains(t, buf.String(), "build "+server+": cargo build --release --locked in server/rs-dev-stack/"+server)
		require.Contains(t, buf.String(), "+ implementation scaffold of "+server+" written to")
	}
	require.Contains(t, buf.String(), fmt.Sprintf(`"msg":"listening","stack":"rs-dev-stack","server":"rs-stock","addr":":%d"`, stockPort))

	base := local.ServerURL(stockPort)
	call := func(method, path, token, body string, status int, want string) string {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		answer, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		require.NoError(t, err)
		require.Equal(t, status, resp.StatusCode, "%s %s: %s\n%s", method, path, answer, buf)
		require.Contains(t, string(answer), want, path)
		return string(answer)
	}
	call(http.MethodGet, "/healthz", "", "", http.StatusOK, `"ok"`)
	call(http.MethodGet, "/readyz", "", "", http.StatusOK, `"ready"`)
	call(http.MethodGet, "/api/levels", "", "", http.StatusNotImplemented, "levels.list_levels is not implemented")
	call(http.MethodPost, "/api/auth/register", "", `{"login": "ada@example.com", "name": "Ada", "password": "ada's password"}`, http.StatusOK, `"token"`)
	var login struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	answer := call(http.MethodPost, "/api/auth/login", "", `{"login": "ada@example.com", "password": "ada's password"}`, http.StatusOK, `"token"`)
	require.NoError(t, json.Unmarshal([]byte(answer), &login))
	call(http.MethodGet, "/api/auth/me", login.Data.Token, "", http.StatusOK, `"login":"ada@example.com"`)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, buf.String())
	case <-time.After(time.Minute):
		t.Fatalf("stack dev did not stop:\n%s", buf)
	}
	require.Contains(t, buf.String(), `"server":"rs-stock"`)
	require.Contains(t, buf.String(), `"msg":"stopped"`)
	require.Contains(t, buf.String(), "removed container "+container)
}
