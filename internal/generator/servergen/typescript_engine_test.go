package servergen_test

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/stackdeploy"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The TypeScript half of the engine fixture (D58): desk-ts-api, desk-api's
// twin in TypeScript over desk-db; desk-ts-stack, which serves it from its
// default server on the local target and runs desk-db's engine runner in
// TypeScript beside it; and desk-ts-cloud-stack, whose Staging places desk-db
// on Cloud SQL.
const (
	engineTSStack      = "desk-ts-stack"
	engineTSCloudStack = "desk-ts-cloud-stack"
	engineTSServer     = "desk-ts-api"
	engineTSJob        = "desk-ts-api-close-stale-tickets"
)

// engineTSOrder is the order a build-all builds the TypeScript engine
// fixture in.
var engineTSOrder = []string{"desk-db", "desk-ts-api", engineTSStack}

// loadTypeScriptEngineFixture loads the TypeScript engine fixture with the
// core registry and stacktest's fake target, as loadEngineFixture does.
func loadTypeScriptEngineFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range append(slices.Clone(engineTSOrder), engineTSCloudStack) {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(engineRoot, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	return f
}

// tsEngineFakePaths are tsFakePaths with the engine's npm package at its
// place in a checkout at repoRoot, as this repository's naming file has it.
func tsEngineFakePaths(repoRoot string) naming.LocalPaths {
	paths := tsFakePaths(repoRoot)
	paths.EngineTypeScript = filepath.Join(repoRoot, "runtime", "engine", "typescript")
	return paths
}

// TestTypeScriptEngineEntrypointGolden: desk-ts-api's TypeScript server
// opens the engine of desk-db in the background through the engine's
// ./server entry, deploys its engine document before /readyz reports it
// ready, mounts its routes at /engine/desk-db behind desk-ts-api's identity
// service and service authenticator, holds it in desk-ts-api's Deps, and
// its Dockerfile builds the engine's Node-API addon in a stage of its own;
// desk-ts-api's job opens it for its Deps and deploys nothing; desk-db's
// runner, in TypeScript, opens the engine and runs its runner as itself,
// holding each engine type (D58). In desk-ts-cloud-stack, whose Staging
// places desk-db on Cloud SQL, each serves the engine a socket over the
// Cloud SQL Node connector's local proxy. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestTypeScriptEngineEntrypointGolden -update
func TestTypeScriptEngineEntrypointGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptEngineFixture(t)
	f.build(t, repoRoot, tsEngineFakePaths(repoRoot), append(slices.Clone(engineTSOrder), engineTSCloudStack)...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	files := map[string]string{}
	all := []string{servergen.TypeScriptMainFile, servergen.TypeScriptPackageFile, servergen.DockerFile, servergen.DockerIgnoreFile}
	for _, d := range []struct {
		stack, name string
		files       []string
	}{
		{engineTSStack, engineTSServer, all},
		{engineTSStack, engineRunner, all},
		{engineTSStack, engineTSJob, []string{servergen.TypeScriptMainFile, servergen.TypeScriptPackageFile}},
		{engineTSCloudStack, engineTSServer, []string{servergen.TypeScriptMainFile, servergen.TypeScriptPackageFile}},
		{engineTSCloudStack, engineRunner, []string{servergen.TypeScriptMainFile, servergen.TypeScriptPackageFile}},
	} {
		for _, file := range d.files {
			files[filepath.Join("server", d.stack, d.name, file)] = filepath.Join(servergen.ServerDir(out, d.stack, d.name), file)
		}
	}
	files[filepath.Join("api", "desk-ts-api", "deps.ts")] = filepath.Join(out, "api", "desk-ts-api", "deps.ts")
	files[filepath.Join("api", "desk-ts-api", "package.json")] = filepath.Join(out, "api", "desk-ts-api", "package.json")
	files["package.json"] = filepath.Join(out, "package.json")
	compareGoldens(t, filepath.Join(goldenRoot, "engine-typescript"), files)
}

// TestATypeScriptEntrypointRefusesAnEngineItsAddonDoesNotRun: the engine's
// Node-API addon runs the core's behaviors alone, so a stack's build
// refuses a TypeScript server, and the runner, on an engine database that
// composes the work queue's Lease, naming the behavior and the project
// engine crate's addon it waits for (D58).
func TestATypeScriptEntrypointRefusesAnEngineItsAddonDoesNotRun(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptEngineFixture(t)
	db := *f.schemas["desk-db"]
	db.Types = maps.Clone(db.Types)
	account := *db.Types["Account"]
	account.Behaviors = append(slices.Clone(account.Behaviors), ir.BehaviorRef{Name: "Lease"})
	db.Types["Account"] = &account
	f.schemas["desk-db"] = &db
	options := f.options(repoRoot, tsEngineFakePaths(repoRoot))
	for _, name := range engineTSOrder {
		_, err := generator.Run(f.schemas[name], f.configs[name], options)
		if name != engineTSStack {
			if err != nil {
				t.Fatalf("build %s: %v", name, err)
			}
			continue
		}
		if want := "composes Lease, which the engine's Node-API addon does not run"; err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("build %s = %v, want it refused saying %q", name, err, want)
		}
	}
}

// requireEngineTypeScript skips unless the engine's npm package at dir is
// built: its dist/, which the workspace's install copies, and the
// Node-API addon for this host in its native/ (scripts/engine-napi.sh).
func requireEngineTypeScript(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "dist", "server", "index.js")); err != nil {
		t.Skipf("the engine's package is not built (bun run build in %s): %v", dir, err)
	}
	triples := map[string]string{"darwin/arm64": "darwin-arm64", "darwin/amd64": "darwin-x64", "linux/amd64": "linux-x64-gnu", "linux/arm64": "linux-arm64-gnu"}
	addon := filepath.Join(dir, "native", "superschematic-engine."+triples[runtime.GOOS+"/"+runtime.GOARCH]+".node")
	if _, err := os.Stat(addon); err != nil {
		t.Skipf("the engine's Node-API addon is not built (run scripts/engine-napi.sh): %v", err)
	}
}

// TestTypeScriptEngineEntrypointServes: desk-ts-api's TypeScript server,
// desk-db's TypeScript runner and desk-ts-api's job install in the output
// root's workspace with the engine's package and type-check. The server
// starts with no database up and answers what needs none: /readyz names
// the engine, its routes under /engine/desk-db answer 503 unavailable
// until it opens, and a trusted origin's preflight of one gets the
// identity service's CORS. With the Postgres engineDatabaseEnv names, the
// server opens the engine on desk-db's database and deploys its document;
// a member who registers is refused a write their roles do not hold, then,
// granted a role holding Account and Ticket, creates an account and reads
// it back, and opens a ticket linked to it; the MCP endpoint answers an
// initialize; a service's credential stands in for an end user, whom the
// permission policy refuses, and one the server's callers field does not
// verify is 401 service_unauthorized. The runner, started beside it,
// fires the ticket's deadline, which escalates it as the runner. Both stop
// cleanly on SIGTERM, the server with an event stream still open (D58).
func TestTypeScriptEngineEntrypointServes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	requireEngineTypeScript(t, paths.EngineTypeScript)
	installTypeScriptRuntime(t, bun, paths)
	repoRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := loadTypeScriptEngineFixture(t)
	f.build(t, repoRoot, paths, engineTSOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	install := exec.Command(bun, "install")
	install.Dir = out
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	dir := servergen.ServerDir(out, engineTSStack, engineTSServer)
	runnerDir := servergen.ServerDir(out, engineTSStack, engineRunner)
	tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
	for _, pkg := range []string{dir, runnerDir, servergen.ServerDir(out, engineTSStack, engineTSJob), naming.Default().TypeScriptImplementationDir(repoRoot, engineTSServer)} {
		cmd := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
		cmd.Dir = pkg
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("tsc in %s: %v\n%s", pkg, err, output)
		}
	}

	identityConfig := ir.IdentityConfigField(engineTSServer) + `={"trustedOrigins": ["https://app.example.com"], "cookie": {"secure": false}, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}`
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	env := append([]string{identityConfig}, deskCallersVariables(t, engineTSServer, public, "ops-key")...)

	offline := bunServer(t, bun, dir, append([]string{"DESK_DB_DATABASE_URL=postgres://desk@127.0.0.1:9/desk_db?connect_timeout=1&sslmode=disable"}, env...)...)
	offline.expect(t, http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"desk-db engine"`)
	offline.expect(t, http.MethodGet, "/engine/desk-db/namespaces/desk-db/schemas", http.StatusServiceUnavailable, `"code":"unavailable"`, "The engine is not available")
	offline.expectPreflight(t, "/engine/desk-db/namespaces/desk-db/schemas/Ticket/instances", http.MethodPost)
	offline.expect(t, http.MethodGet, "/api/summary", http.StatusUnauthorized)
	offline.stop(t)
	if !strings.Contains(offline.out.String(), `"msg":"engine not open"`) {
		t.Errorf("the server did not log the engine's failed opens:\n%s", offline.out.String())
	}

	postgres := os.Getenv(engineDatabaseEnv)
	if postgres == "" {
		t.Logf("%s is unset: the entrypoint serves no database", engineDatabaseEnv)
		return
	}
	tool := engineDatabaseToolTS(t, bun, dir)
	databaseURL := tool.create(t, postgres, filepath.Join(generator.SQLDir(out, "desk-db"), "create.sql"))
	online := bunServer(t, bun, dir, append([]string{"DESK_DB_DATABASE_URL=" + databaseURL}, env...)...)
	online.waitReady(t)
	online.send(t, http.MethodPost, "/api/auth/register", `{"login": "ada@example.com", "name": "Ada", "password": "ada's password"}`, http.StatusOK, `"token"`)
	token := online.login(t, "ada@example.com", "ada's password")
	const accounts = "/engine/desk-db/namespaces/desk-db/schemas/Account/instances"
	const tickets = "/engine/desk-db/namespaces/desk-db/schemas/Ticket/instances"
	online.engine(t, token, http.MethodPost, accounts, `{"data": {"name": "Acme"}}`, http.StatusForbidden, `"code":"forbidden"`)
	tool.grant(t, databaseURL, "ada@example.com", "desk", "Account", "Ticket")
	account := online.engine(t, token, http.MethodPost, accounts, `{"id": "acme", "data": {"name": "Acme"}}`, http.StatusCreated, `"id":"acme"`)
	if !strings.Contains(account, `"createdBy":"`) || strings.Contains(account, `"createdBy":"desk-db-engine-runner"`) {
		t.Errorf("the account was created as %s", account)
	}
	online.engine(t, token, http.MethodGet, accounts+"/acme", "", http.StatusOK, `"name":"Acme"`, `"Comments":{"commentCount":0}`)
	online.engine(t, token, http.MethodPost, tickets, `{"id": "t1", "data": {"title": "The printer is out of toner", "contact": {"email": "bob@example.com"}}, "behaviors": {"Links": {"account": "acme"}}}`,
		http.StatusCreated, `"status":"open"`, `"respond":{"dueAt":`)
	// The engine's MCP endpoint, mounted beside its routes.
	online.mcp(t, token, "/engine/desk-db/namespaces/desk-db/mcp", `{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "servergen", "version": "1"}}}`, `"serverInfo"`)
	// A service's credential with no session stands in for an end user,
	// who holds no permission; one the callers field does not verify is
	// refused before the engine sees it.
	online.service(t, signEdgeToken(t, key, "ops-key", "ops", engineTSServer), http.MethodGet, accounts+"/acme", http.StatusForbidden, `"code":"forbidden"`, "service:ops")
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	online.service(t, signEdgeToken(t, other, "ops-key", "ops", engineTSServer), http.MethodGet, accounts+"/acme", http.StatusUnauthorized, `"code":"service_unauthorized"`)

	runner := bunBackground(t, bun, runnerDir, "DESK_DB_DATABASE_URL="+databaseURL)
	deadline := time.Now().Add(30 * time.Second)
	for {
		ticket := online.engine(t, token, http.MethodGet, tickets+"/t1", "", http.StatusOK)
		if strings.Contains(ticket, `"status":"escalated"`) {
			for _, want := range []string{`"fired":true`, `"outcome":"moved"`, `"updatedBy":"desk-db-engine-runner"`} {
				if !strings.Contains(ticket, want) {
					t.Errorf("the escalated ticket lacks %s: %s", want, ticket)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the runner did not fire the ticket's deadline: %s\n%s", ticket, runner.out.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
	runner.stop(t)
	// An event stream still open at SIGTERM ends as the server stops
	// taking requests, so the shutdown waits for none.
	stream := online.eventStream(t, token, "/engine/desk-db/namespaces/desk-db/events?after=0")
	online.stop(t)
	stream.Close()
}

// mcp posts a JSON-RPC request to the MCP endpoint at path with the bearer
// token and checks that the answer holds each of contains.
func (s *started) mcp(t *testing.T, token, path, body string, contains ...string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("POST %s = %d: %s", path, resp.StatusCode, answer)
	}
	for _, want := range contains {
		if !strings.Contains(string(answer), want) {
			t.Errorf("POST %s answered %s, which lacks %q", path, answer, want)
		}
	}
}

// eventStream opens the engine's event stream at path with the bearer
// token and returns its body once the stream reports it is ready.
func (s *started) eventStream(t *testing.T, token, path string) io.ReadCloser {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		answer, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("GET %s = %d: %s", path, resp.StatusCode, answer)
	}
	lines := bufio.NewScanner(resp.Body)
	for lines.Scan() {
		if lines.Text() == "event: ready" {
			return resp.Body
		}
	}
	resp.Body.Close()
	t.Fatalf("the event stream at %s ended before it was ready: %v", path, lines.Err())
	return nil
}

// bunBackground runs main.ts in dir under Bun, which listens on no port,
// with env, and waits for it to log that it started its runner.
func bunBackground(t *testing.T, bun, dir string, env ...string) *started {
	t.Helper()
	out := &syncBuilder{}
	cmd := exec.Command(bun, servergen.TypeScriptMainFile)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &started{cmd: cmd, out: &out.Builder}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(30 * time.Second)
	for !strings.Contains(out.String(), `"msg":"runner started"`) {
		if time.Now().After(deadline) {
			t.Fatalf("main.ts in %s did not start its runner:\n%s", dir, out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	return s
}

// engineDatabaseToolsTS runs engine-database.ts with Bun in the TypeScript
// server's package at dir, which depends on pg: create, drop and grant, as
// engineDatabaseTools does with Go.
type engineDatabaseToolsTS struct {
	bun, dir, script string
}

func engineDatabaseToolTS(t *testing.T, bun, dir string) engineDatabaseToolsTS {
	t.Helper()
	script := filepath.Join(t.TempDir(), "engine-database.ts")
	if err := os.WriteFile(script, []byte(engineDatabaseScript), 0o644); err != nil {
		t.Fatal(err)
	}
	return engineDatabaseToolsTS{bun: bun, dir: dir, script: script}
}

func (e engineDatabaseToolsTS) run(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(e.bun, append([]string{"run", e.script}, args...)...)
	cmd.Dir = e.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("engine-database.ts %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// create makes a database of its own on the Postgres at base, applies
// createSQL to it, drops it when the test ends, and returns its URL.
func (e engineDatabaseToolsTS) create(t *testing.T, base, createSQL string) string {
	t.Helper()
	name := fmt.Sprintf("engine_ts_entrypoint_%d", time.Now().UnixNano())
	url := e.run(t, "create", base, name, createSQL)
	t.Cleanup(func() { e.run(t, "drop", base, name) })
	return url
}

// grant gives the member whose login is login the role named role, which
// holds permissions.
func (e engineDatabaseToolsTS) grant(t *testing.T, url, login, role string, permissions ...string) {
	t.Helper()
	e.run(t, append([]string{"grant", url, login, role}, permissions...)...)
}

// engineDatabaseScript is engine-database.ts: create base name create.sql
// makes a database of its own on the Postgres at base, applies create.sql
// to it and prints its URL; drop base name drops it; grant url login role
// permission... gives the member whose login is login a role holding the
// permissions. It imports pg from the package it runs in.
const engineDatabaseScript = `import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { join } from 'node:path';

const pg = createRequire(join(process.cwd(), 'package.json'))('pg');
const [command, ...args] = process.argv.slice(2);
const connect = async (url) => {
  const client = new pg.Client({ connectionString: url });
  await client.connect();
  return client;
};
if (command === 'create') {
  const [base, name, ddl] = args;
  const admin = await connect(base);
  try {
    await admin.query('CREATE DATABASE ' + name);
  } finally {
    await admin.end();
  }
  const url = new URL(base);
  url.pathname = '/' + name;
  const client = await connect(url.toString());
  try {
    for (const extension of ['pgcrypto', 'citext']) {
      await client.query('CREATE EXTENSION IF NOT EXISTS ' + extension);
    }
    await client.query(readFileSync(ddl, 'utf8'));
  } finally {
    await client.end();
  }
  console.log(url.toString());
} else if (command === 'drop') {
  const [base, name] = args;
  const admin = await connect(base);
  try {
    await admin.query('DROP DATABASE ' + name + ' WITH (FORCE)');
  } finally {
    await admin.end();
  }
} else if (command === 'grant') {
  const [url, login, role, ...permissions] = args;
  const client = await connect(url);
  try {
    await client.query('INSERT INTO member_role (name, permissions) VALUES ($1, $2)', [role, permissions]);
    const granted = await client.query(
      'INSERT INTO user_role_grant (user_id, role_id) SELECT m.id, r.id FROM member m, member_role r WHERE m.email = $1 AND r.name = $2',
      [login, role]
    );
    if (granted.rowCount !== 1) throw new Error('no member has the login ' + login);
  } finally {
    await client.end();
  }
} else {
  throw new Error('unknown command ' + command);
}
`

// TestTheTypeScriptEngineDockerfileBuildsAnImageThatServes: desk-ts-api's
// Dockerfile builds from the context a deploy writes
// (stackdeploy.WriteContext) out of a repository root holding the
// generated packages, the scaffold, the installed workspace's lockfile,
// and checkouts of superscalar, the HTTP runtime, the engine's package and
// the Cargo workspace beside it with the crates it reaches: its engine
// stage builds the engine's Node-API addon, and its build stage emits the
// package's ./server entry (D58). The image serves /healthz, names the
// engine in /readyz with no database up, answers the engine's routes 503,
// and stops cleanly on docker stop. It needs Docker and the network, builds
// superscalar's addon and the engine's in Rust stages, and runs only
// outside -short.
func TestTheTypeScriptEngineDockerfileBuildsAnImageThatServes(t *testing.T) {
	needDocker(t)
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skipf("bun not available: %v", err)
	}
	local := testpaths.Local(t)
	repo := testpaths.RepoRoot(t)
	repoRoot := t.TempDir()
	// The checkouts, without what a checkout builds, which the image builds
	// again.
	copyTree(t, local.HTTPRuntimeTypeScript, filepath.Join(repoRoot, "runtime", "http", "typescript"))
	copyTree(t, filepath.Join(repo, "third_party", "superscalar", "crates"), filepath.Join(repoRoot, "third_party", "superscalar", "crates"))
	copyTree(t, local.ScalarTypeScript, filepath.Join(repoRoot, "third_party", "superscalar", "bindings", "typescript"))
	for _, file := range []string{"Cargo.toml", "Cargo.lock"} {
		data, err := os.ReadFile(filepath.Join(repo, "third_party", "superscalar", file))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repoRoot, "third_party", "superscalar", file), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	copyTree(t, local.EngineTypeScript, filepath.Join(repoRoot, "runtime", "engine", "typescript"), "dist", "native")
	for _, dir := range []string{"runtime/engine/rust", "runtime/engine-workqueue/rust", "runtime/schema/rust-ir", "runtime/schema/rust", "runtime/versiongraph/rust-engine", "runtime/versiongraph/rust"} {
		copyTree(t, filepath.Join(repo, filepath.FromSlash(dir)), filepath.Join(repoRoot, filepath.FromSlash(dir)))
	}
	// The engine's package depends on the version graph's by a path beside
	// it, which the workspace's root overrides with this one.
	versionGraph := filepath.Join(repoRoot, "runtime", "versiongraph", "typescript")
	if err := os.MkdirAll(versionGraph, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(local.VersionGraphTypeScript, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versionGraph, "package.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	f := loadTypeScriptEngineFixture(t)
	f.build(t, repoRoot, tsEngineFakePaths(repoRoot), engineTSOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	install := exec.Command(bun, "install")
	install.Dir = out
	if output, err := install.CombinedOutput(); err != nil {
		t.Skipf("bun install failed at the output root (likely offline): %v\n%s", err, output)
	}

	dockerfile := filepath.Join(servergen.ServerDir(out, engineTSStack, engineTSServer), servergen.DockerFile)
	archive := filepath.Join(t.TempDir(), "context.tar.gz")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	built, err := stackdeploy.WriteContext(file, repoRoot, dockerfile)
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatalf("write the build context: %v", err)
	}
	context, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer context.Close()
	image := fmt.Sprintf("superschematic-servergen-ts-test:%d-engine", time.Now().UnixNano())
	build := exec.Command("docker", "build", "-q", "-t", image, "-f", built.Dockerfile, "-")
	build.Stdin = context
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", image).Run() })

	container := docker(t, "run", "-d", "-p", "127.0.0.1::8080",
		"-e", "DESK_DB_DATABASE_URL=postgres://desk@127.0.0.1:9/desk_db?connect_timeout=1&sslmode=disable",
		"-e", "DESK_TS_API_CALLERS_ISSUERS=0", image)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", container).Run() })
	address := docker(t, "port", container, "8080/tcp")
	if i := strings.Index(address, "\n"); i >= 0 {
		address = address[:i]
	}
	s := &started{base: "http://" + address, out: &strings.Builder{}}
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get(s.base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the container did not answer /healthz: %v\n%s", err, docker(t, "logs", container))
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.expect(t, http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"desk-db engine"`)
	s.expect(t, http.MethodGet, "/engine/desk-db/namespaces/desk-db/schemas", http.StatusServiceUnavailable, `"code":"unavailable"`)
	docker(t, "stop", container)
	if code := docker(t, "inspect", "-f", "{{.State.ExitCode}}", container); code != "0" {
		t.Errorf("the container exited %s on docker stop:\n%s", code, docker(t, "logs", container))
	}
	if logs := docker(t, "logs", container); !strings.Contains(logs, `"msg":"engine not open"`) || !strings.Contains(logs, `"msg":"stopped"`) {
		t.Errorf("the container did not log the engine's failed open and its graceful stop:\n%s", logs)
	}
}
