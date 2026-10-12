package local_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
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
// signs a token with the HTTP runtime's serviceauth.SignedToken from its
// derived credential variables, and the token verifies against the public
// key of the edge's key pair (D37). ledger-api's bucket is on the storage
// emulator, whose container the run starts beside Postgres's (D54). Run
// again, it reuses the containers, which kept their data, the database's
// and the bucket's, and migrates to the next model. Purge removes the
// containers.
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
	pgPort, storagePort, apiPort, workerPort := freePort(t), freePort(t), freePort(t), freePort(t)
	db := ir.ServiceRef{Name: "ledger-db", Kind: ir.SchemaKindDB}
	files := ir.ServiceRef{Name: "ledger-files", Kind: ir.SchemaKindBucket}
	api := ir.ServiceRef{Name: "ledger-api", Kind: ir.SchemaKindAPI}
	worker := ir.ServiceRef{Name: "ledger-worker", Kind: ir.SchemaKindAPI}
	greeting := "hello"
	services := []stack.Service{
		{Name: "ledger-db", Kind: ir.SchemaKindDB},
		{Name: "ledger-files", Kind: ir.SchemaKindBucket},
		{
			Name: "ledger-api", Kind: ir.SchemaKindAPI, AuthDB: &db, Buckets: []ir.ServiceRef{files},
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
			Values: map[string]any{"postgresPort": float64(pgPort), "storagePort": float64(storagePort)},
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
	container, storage := local.ContainerName(stackName, "Dev"), local.StorageContainerName(stackName, "Dev")
	emulator := local.StorageURL(storagePort)

	root := t.TempDir()
	outputRoot := filepath.Join(root, "dist")
	for _, server := range []string{"ledger-api", "ledger-worker"} {
		copyFakeServer(t, filepath.Join(outputRoot, local.ModulePath(stackName, server)))
	}
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
		_ = exec.Command("docker", "rm", "--force", "--volumes", container, storage).Run()
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
		"PORT":                         fmt.Sprint(apiPort),
		"GREETING":                     "hello",
		"TOKEN":                        "s3cret",
		"LEDGER_DB_DATABASE_URL":       local.DatabaseURL(pgPort, "ledger_db"),
		"LEDGER_FILES_BUCKET_NAME":     "ledger-files",
		"LEDGER_FILES_BUCKET_ENDPOINT": emulator,
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

	// The worker signs a token with its edge's key, which verifies against
	// the key pair's public key, for ledger-api as the worker.
	resp, err = http.Get(local.ServerURL(workerPort) + "/token/LEDGER_API_SERVICE")
	if err != nil {
		t.Fatal(err)
	}
	token, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the worker's token: %d %s", resp.StatusCode, token)
	}
	outputs, err := prov.Outputs(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	checkToken(t, string(token), fmt.Sprint(outputs["ledger-worker.calls.ledger-api.key"]["publicJwk"]), "ledger-worker", "ledger-api")

	// The provisioner created ledger-api's bucket on the emulator, which
	// takes an object.
	upload, err := http.Post(emulator+"/upload/storage/v1/b/ledger-files/o?uploadType=media&name=kept.txt", "text/plain", strings.NewReader("kept"))
	if err != nil {
		t.Fatal(err)
	}
	_ = upload.Body.Close()
	if upload.StatusCode != http.StatusOK {
		t.Fatalf("upload to ledger-files: %s", upload.Status)
	}

	// Destroy stops the server and the containers, which keep their data;
	// the next run starts them again, keeps the bucket's object, and
	// migrates to the next model.
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
	kept, err := http.Get(emulator + "/storage/v1/b/ledger-files/o/kept.txt")
	if err != nil {
		t.Fatal(err)
	}
	_ = kept.Body.Close()
	if kept.StatusCode != http.StatusOK || !strings.Contains(out.String(), "bucket ledger-files exists") {
		t.Errorf("the second run lost ledger-files' object (%s) or created the bucket again:\n%s", kept.Status, out)
	}

	if err := prov.Purge(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{container, storage} {
		if err := exec.Command("docker", "container", "inspect", name).Run(); err == nil {
			t.Errorf("container %s is still there after Purge", name)
		}
	}
}

// TestLocalTypeScriptServerRuns applies a local environment of one
// TypeScript server for real, with Bun and no Docker: the provisioner
// installs the output root's Bun workspace, which brings a stale lockfile
// up to date as a developer's install does, runs a fake server's main.ts
// that honours the entrypoint's contract, waits for /readyz, and the
// server reads its resolved environment and no more of the test's. Destroy
// stops it with SIGTERM, which it answers before it exits.
func TestLocalTypeScriptServerRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: the local stack runs a server on Bun")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skipf("bun is not available: %v", err)
	}
	stackName := fmt.Sprintf("local-target-ts-it-%d", os.Getpid())
	port := freePort(t)
	web := ir.ServiceRef{Name: "ledger-web", Kind: ir.SchemaKindAPI}
	greeting := "hello"
	services := []stack.Service{{
		Name: "ledger-web", Kind: ir.SchemaKindAPI, Language: registry.APILanguageTypeScript,
		Config: &stack.Config{
			Type:   "WebConfig",
			Fields: []stack.ConfigField{{Name: "GREETING", Required: true, Default: &greeting}},
		},
		Operations: []stack.Operation{{Name: "Pages.getHome"}},
	}}
	st := &ir.Stack{
		Name:   stackName,
		Deploy: []ir.ServiceRef{web},
		Environments: []*ir.Environment{{
			Name:     "Dev",
			Target:   local.Target,
			Settings: []*ir.DeployableSettings{{Of: ir.DeployableRef{Service: &web}, Values: map[string]any{"port": float64(port)}}},
		}},
	}
	reg, err := registry.Assemble(registry.DefaultNaming())
	if err != nil {
		t.Fatal(err)
	}
	env, err := stack.Resolve(reg, stack.Input{Stack: st, Services: services, Environment: "Dev"})
	if err != nil {
		t.Fatal(err)
	}

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outputRoot := filepath.Join(root, "dist")
	if err := os.CopyFS(filepath.Join(outputRoot, local.ModulePath(stackName, "ledger-web")), os.DirFS(filepath.Join("testdata", "faketsserver"))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputRoot, "package.json"), []byte(`{"name": "ledger-workspace", "private": true, "workspaces": ["server/*/*"]}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A lockfile from before the server joined the workspace, as a project
	// commits it (D51, amended): the install brings it up to date as a
	// developer's would, even where CI is set, which a frozen install would
	// refuse.
	stale := "{\n  \"lockfileVersion\": 2,\n  \"configVersion\": 1,\n  \"workspaces\": {\n    \"\": {\n      \"name\": \"ledger-workspace\",\n    },\n  },\n  \"packages\": {},\n}\n"
	if err := os.WriteFile(filepath.Join(outputRoot, "bun.lock"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CI", "true")
	stateDir, err := local.EnsureStateDir(filepath.Join(root, "schemas"), stackName, "Dev")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("LEDGER_LEAK", "leaked")

	out := &syncBuffer{}
	prov := &local.Provisioner{Out: out, ReadyTimeout: time.Minute}
	dir := filepath.Join(root, "program")
	req := registry.ProvisionRequest{Environment: env, Dir: dir, OutputRoot: outputRoot, Backend: local.StateBackend(stateDir)}
	t.Cleanup(func() {
		_ = prov.Destroy(context.Background(), req)
		if t.Failed() || testing.Verbose() {
			t.Logf("provisioner output:\n%s", out)
		}
	})
	if err := prov.Render(env, dir); err != nil {
		t.Fatal(err)
	}
	for _, step := range env.DeployOrder {
		if err := prov.Apply(context.Background(), req, *step); err != nil {
			t.Fatal(err)
		}
	}
	if lock, err := os.ReadFile(filepath.Join(outputRoot, "bun.lock")); err != nil || !strings.Contains(string(lock), `"fake-ts-server@workspace:server/`+stackName+`/ledger-web"`) {
		t.Errorf("the provisioner's install did not bring the lockfile up to date with the server: %v\n%s", err, lock)
	}

	resp, err := http.Get(local.ServerURL(port) + "/env")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	err = json.NewDecoder(resp.Body).Decode(&got)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got["PORT"] != fmt.Sprint(port) || got["GREETING"] != "hello" {
		t.Errorf("the server got PORT=%q GREETING=%q, want %d and hello", got["PORT"], got["GREETING"], port)
	}
	if _, leaked := got["LEDGER_LEAK"]; leaked {
		t.Error("the server inherited a variable its environment does not bind")
	}

	if err := prov.Destroy(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := http.Get(local.ServerURL(port) + "/readyz"); err == nil {
		t.Error("the server still answers after Destroy")
	}
	for _, want := range []string{
		"[ledger-web] fake TypeScript server listening on 127.0.0.1:" + fmt.Sprint(port),
		"[ledger-web] fake TypeScript server stopped",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// checkToken checks a compact JWS as D37 says the callee does: its header's
// alg is EdDSA and its kid the key's, its signature verifies with the
// public JWK, iss and sub are the caller, aud the callee, and exp is 300
// seconds after iat.
func checkToken(t *testing.T, token, publicJWK, caller, callee string) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q is not a compact JWS", token)
	}
	var key local.JWK
	if err := json.Unmarshal([]byte(publicJWK), &key); err != nil {
		t.Fatalf("public key: %v", err)
	}
	decode := func(part string, v any) {
		t.Helper()
		data, err := base64.RawURLEncoding.DecodeString(part)
		if err != nil {
			t.Fatal(err)
		}
		if v != nil {
			if err := json.Unmarshal(data, v); err != nil {
				t.Fatal(err)
			}
		}
	}
	var header struct{ Alg, Kid string }
	var claims struct {
		Iss, Sub, Aud string
		Iat, Exp      int64
	}
	decode(parts[0], &header)
	decode(parts[1], &claims)
	x, err := base64.RawURLEncoding.DecodeString(key.X)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(x, []byte(parts[0]+"."+parts[1]), signature) {
		t.Error("the token's signature does not verify with the edge's public key")
	}
	if header.Alg != "EdDSA" || header.Kid != key.Kid || claims.Iss != caller || claims.Sub != caller || claims.Aud != callee || claims.Exp-claims.Iat != 300 {
		t.Errorf("token header %+v, claims %+v; want EdDSA with the key's kid, iss and sub %s, aud %s, five minutes", header, claims, caller, callee)
	}
}
