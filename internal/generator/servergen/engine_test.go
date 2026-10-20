package servergen_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/permcatalog"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

const (
	// engineRoot holds the engine fixture (D58): desk-db, an engine
	// database whose Account and Ticket compose behaviors, beside the user
	// model's tables; desk-api, a public Go API over it with the session
	// routes and a route of its own; and desk-stack, which serves desk-api
	// from its default server on the local target, and runs desk-db's
	// engine runner beside it.
	engineRoot = "testdata/engine"

	// engineStack is the fixture's stack, engineServer its server and
	// engineRunner the engine's runner of desk-db.
	engineStack  = "desk-stack"
	engineServer = "desk-api"
	engineRunner = "desk-db-engine-runner"
	engineJob    = "desk-api-close-stale-tickets"

	// engineDatabaseEnv names the Postgres the engine fixture's server and
	// runner also run on, as the identity fixture's entrypoint test reads
	// it.
	engineDatabaseEnv = identityDatabaseEnv
)

// engineOrder is the order a build-all builds the engine fixture in;
// engineCloudStack is a second stack of it, whose Staging places desk-db
// on Cloud SQL.
var engineOrder = []string{"desk-db", "desk-api", engineStack}

const engineCloudStack = "desk-cloud-stack"

// queueOrder is the order a build-all builds the queue fixture in: queue-db,
// an engine database whose Job composes the work queue's behaviors, which
// the core's engine archive leaves out; queue-api, a Go API over it whose
// operations only a calling service may call; and queue-stack, which serves
// queue-api on the local target beside queue-db's runner (D58).
var queueOrder = []string{"queue-db", "queue-api", queueStack}

// rustEngineOrder is the order a build-all builds the Rust engine fixture
// in: desk-db, desk-rs-api, desk-api's twin in Rust, and desk-rs-stack,
// which serves desk-rs-api on the local target beside desk-db's runner,
// in Rust (D58).
var rustEngineOrder = []string{"desk-db", "desk-rs-api", rustEngineStack}

// rustEngineStack is the Rust engine fixture's stack, rustEngineServer its
// server and rustEngineJob desk-rs-api's job; its runner is engineRunner.
const (
	rustEngineStack  = "desk-rs-stack"
	rustEngineServer = "desk-rs-api"
	rustEngineJob    = "desk-rs-api-close-stale-tickets"
)

// queueStack is the queue fixture's stack, queueServer its server and
// queueRunner the engine's runner of queue-db.
const (
	queueStack  = "queue-stack"
	queueServer = "queue-api"
	queueRunner = "queue-db-engine-runner"
)

// loadEngineFixture loads the engine fixture with the core registry, whose
// local target the stacks' environments run on, and stacktest's fake
// target, whose sql connector derives a Cloud SQL connector configuration
// as the gcp target's does.
func loadEngineFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range append(append(append(slices.Clone(engineOrder), engineCloudStack), queueOrder...), "desk-rs-api", rustEngineStack) {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(engineRoot, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	return f
}

// TestTheEngineDatabaseOfAnAPI: an API whose database is an engine
// database lists, in its permission catalog, what the engine's permission
// policy asks of each engine type, a version 2 catalog beside the
// operations' permissions; and the stack resolves desk-db's runner in
// desk-api's Go, on the local runner platform, with its database's
// connection as its one binding (D58).
func TestTheEngineDatabaseOfAnAPI(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadEngineFixture(t)
	f.build(t, repoRoot, fakePaths(repoRoot), engineOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")

	data, err := os.ReadFile(filepath.Join(generator.APIDir(out, "desk-api"), permcatalog.FileName))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := permcatalog.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Version != permcatalog.EngineVersion || catalog.AuthDB != "desk-db" {
		t.Errorf("catalog = version %d over %s", catalog.Version, catalog.AuthDB)
	}
	var engine []string
	for _, p := range catalog.Permissions {
		if p.Engine != nil {
			if p.Engine.Database != "desk-db" || p.Engine.Namespace != "desk-db" || p.Name != p.Engine.Schema+"."+p.Engine.Action {
				t.Errorf("engine permission %+v", p)
			}
			engine = append(engine, p.Name)
		}
	}
	want := []string{"Account.define", "Account.publish", "Account.read", "Account.write", "Ticket.define", "Ticket.publish", "Ticket.read", "Ticket.write"}
	if !slices.Equal(engine, want) {
		t.Errorf("engine permissions = %v, want %v", engine, want)
	}

	data, err = os.ReadFile(filepath.Join(out, "stack", engineStack, "Local", "environment.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env ir.ResolvedEnvironment
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	runner := env.Deployable(engineRunner)
	if runner == nil || runner.Kind != ir.DeployableRunner || runner.Language != "GO" || runner.Platform != "local.runner" ||
		runner.Runner == nil || runner.Runner.Database != "desk-db" || runner.Runner.API != "desk-api" || runner.Runner.Instances != 1 {
		t.Fatalf("the runner is %+v", runner)
	}
	var fields []string
	for _, b := range runner.Bindings {
		fields = append(fields, b.Field+"="+string(b.Source))
	}
	if got := strings.Join(fields, " "); got != "DESK_DB_DATABASE=derived" {
		t.Errorf("the runner binds %s, want its database alone", got)
	}
}

// TestEngineEntrypointGolden: desk-api's server opens the engine of
// desk-db in the background, deploys its engine document before /readyz
// reports it ready, mounts its routes on desk-api's router at
// /engine/desk-db behind the identity runtime's session and the service
// authenticator, holds it in desk-api's Deps, and its checkout Dockerfile
// builds the engine's archive; desk-api's job opens it for its Deps and
// deploys nothing; desk-db's runner opens the engine and runs its runner,
// as itself, holding each engine type (D58). In
// desk-cloud-stack, whose Staging places desk-db on Cloud SQL, each serves
// the engine a socket over the Cloud SQL connector. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestEngineEntrypointGolden -update
func TestEngineEntrypointGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadEngineFixture(t)
	f.build(t, repoRoot, fakePaths(repoRoot), append(slices.Clone(engineOrder), engineCloudStack)...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	files := map[string]string{}
	for _, d := range []string{engineServer, engineRunner} {
		files[filepath.Join("server", engineCloudStack, d, servergen.CloudSQLFile)] = filepath.Join(servergen.ServerDir(out, engineCloudStack, d), servergen.CloudSQLFile)
	}
	for _, d := range []struct {
		name  string
		files []string
	}{
		{engineServer, []string{servergen.MainFile, servergen.EngineFile, servergen.ServiceAuthFile, servergen.ModFile, servergen.DockerFile, servergen.DockerIgnoreFile}},
		{engineRunner, []string{servergen.MainFile, servergen.EngineFile, servergen.ModFile, servergen.DockerFile, servergen.DockerIgnoreFile}},
		{engineJob, []string{servergen.MainFile, servergen.EngineFile}},
	} {
		for _, file := range d.files {
			files[filepath.Join("server", engineStack, d.name, file)] = filepath.Join(servergen.ServerDir(out, engineStack, d.name), file)
		}
	}
	files[filepath.Join("api", "desk-api", "deps.go")] = filepath.Join(generator.APIDir(out, "desk-api"), "deps.go")
	compareGoldens(t, filepath.Join(goldenRoot, "engine"), files)
}

// TestEngineEntrypointServes: desk-api's server and desk-db's runner build
// and vet, linking the engine's archive, and so does desk-api's job. The server starts with no
// database up and answers what needs none: /readyz names the engine, its
// routes under /engine/desk-db answer 503 unavailable until it opens, and
// a trusted origin's preflight of one gets the identity runtime's CORS.
// With the Postgres engineDatabaseEnv names, the server opens the engine
// on desk-db's database and deploys its document; a member who registers
// is refused a write their roles do not hold, then, granted a role holding
// Account and Ticket, creates an account and reads it back, and opens a
// ticket linked to it; a service's credential stands in for an end user,
// whom the permission policy refuses, and one the server's callers field
// does not verify is 401 service_unauthorized. The runner, started beside
// it, fires the ticket's deadline, which escalates it as the runner. Both
// stop cleanly on SIGTERM.
func TestEngineEntrypointServes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	requireEngineArchive(t, paths)
	repoRoot := t.TempDir()
	f := loadEngineFixture(t)
	f.build(t, repoRoot, paths, engineOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	dir := servergen.ServerDir(out, engineStack, engineServer)
	goCommand(t, dir, "mod", "tidy")
	goCommand(t, dir, "vet", ".")
	binary := filepath.Join(t.TempDir(), engineServer)
	goCommand(t, dir, "build", "-o", binary, ".")
	runnerDir := servergen.ServerDir(out, engineStack, engineRunner)
	goCommand(t, runnerDir, "mod", "tidy")
	goCommand(t, runnerDir, "vet", ".")
	runnerBinary := filepath.Join(t.TempDir(), engineRunner)
	goCommand(t, runnerDir, "build", "-o", runnerBinary, ".")
	// desk-api's job opens the engine for its Deps too.
	jobDir := servergen.ServerDir(out, engineStack, engineJob)
	goCommand(t, jobDir, "mod", "tidy")
	goCommand(t, jobDir, "vet", ".")

	identityConfig := `{"trustedOrigins": ["https://app.example.com"], "cookie": {"secure": false}, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}`
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	env := append([]string{"DESK_API_IDENTITY=" + identityConfig}, deskCallersVariables(t, public, "ops-key")...)

	offline := start(t, binary, append([]string{"DESK_DB_DATABASE_URL=postgres://desk@127.0.0.1:9/desk_db?connect_timeout=1&sslmode=disable"}, env...)...)
	offline.expect(t, http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"desk-db engine"`)
	offline.expect(t, http.MethodGet, "/engine/desk-db/namespaces/desk-db/schemas", http.StatusServiceUnavailable, `"code":"unavailable"`, "The engine is not available")
	offline.expectPreflight(t, "/engine/desk-db/namespaces/desk-db/schemas/Ticket/instances", http.MethodPost)
	offline.expect(t, http.MethodGet, "/api/summary", http.StatusUnauthorized)
	// The first attempt to open the engine fails in the background, and is
	// logged once it does, which a loaded machine may take a while to get to.
	for wait := time.Now().Add(10 * time.Second); !strings.Contains(offline.out.String(), `"msg":"engine not open"`); {
		if time.Now().After(wait) {
			t.Errorf("the server did not log the engine's failed opens:\n%s", offline.out.String())
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	offline.stop(t)

	postgres := engineDatabase(t)
	if postgres == "" {
		t.Logf("%s is unset: the entrypoint serves no database", engineDatabaseEnv)
		return
	}
	tool := engineDatabaseTool(t, dir)
	databaseURL := tool.create(t, postgres, filepath.Join(generator.SQLDir(out, "desk-db"), "create.sql"))
	online := start(t, binary, append([]string{"DESK_DB_DATABASE_URL=" + databaseURL}, env...)...)
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
	// A service's credential with no session stands in for an end user,
	// who holds no permission; one the callers field does not verify is
	// refused before the engine sees it.
	online.service(t, signEdgeToken(t, key, "ops-key", "ops", "desk-api"), http.MethodGet, accounts+"/acme", http.StatusForbidden, `"code":"forbidden"`, "service:ops")
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	online.service(t, signEdgeToken(t, other, "ops-key", "ops", "desk-api"), http.MethodGet, accounts+"/acme", http.StatusUnauthorized, `"code":"service_unauthorized"`)

	runner := startBackground(t, runnerBinary, "DESK_DB_DATABASE_URL="+databaseURL)
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
	online.stop(t)
}

// deskCallersVariables are desk-api's callers field as an environment sets
// it, ops an issuer of its own with an Ed25519 key: a caller that serves
// ops-api.
func deskCallersVariables(t *testing.T, public ed25519.PublicKey, kid string) []string {
	t.Helper()
	jwk, err := json.Marshal(map[string]string{"kty": "OKP", "crv": "Ed25519", "kid": kid, "x": base64.RawURLEncoding.EncodeToString(public)})
	if err != nil {
		t.Fatal(err)
	}
	vars, err := ir.DerivedVariables(ir.CallersField("desk-api"), ir.ServiceAuth{Issuers: []*ir.ServiceAuthIssuer{{
		Issuer: "ops", Audience: "desk-api", Algorithms: []string{ir.AlgorithmEdDSA},
		Keys:               []ir.ServiceAuthKey{{JWK: string(jwk)}},
		MaxLifetimeSeconds: 300,
		Callers:            []ir.ServiceAuthCaller{{Subject: "ops", Deployable: "ops", Serves: []string{"ops-api"}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	env := make([]string, len(vars))
	for i, v := range vars {
		env[i] = fmt.Sprintf("%s=%v", v.Name, v.Value)
	}
	return env
}

// waitReady waits for /readyz to answer 200: the engine open and its
// document deployed.
func (s *started) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get(s.base + "/readyz")
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("/readyz = %d %s\n%s", resp.StatusCode, body, s.out.String())
			}
		} else if time.Now().After(deadline) {
			t.Fatalf("/readyz: %v\n%s", err, s.out.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// engine sends method to path with the bearer token and body, a JSON one
// unless empty, checks the status and that the answer holds each of
// contains, and returns the answer.
func (s *started) engine(t *testing.T, token, method, path, body string, status int, contains ...string) string {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, s.base+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != status {
		t.Errorf("%s %s = %d, want %d: %s", method, path, resp.StatusCode, status, answer)
	}
	for _, want := range contains {
		if !strings.Contains(string(answer), want) {
			t.Errorf("%s %s answered %s, which lacks %q", method, path, answer, want)
		}
	}
	return string(answer)
}

// service sends method to path with a service credential and no end
// user's, and checks the status and the answer.
func (s *started) service(t *testing.T, credential, method, path string, status int, contains ...string) {
	t.Helper()
	req, err := http.NewRequest(method, s.base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Service-Authorization", "Bearer "+credential)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != status {
		t.Errorf("%s %s with a service credential = %d, want %d: %s", method, path, resp.StatusCode, status, answer)
	}
	for _, want := range contains {
		if !strings.Contains(string(answer), want) {
			t.Errorf("%s %s answered %s, which lacks %q", method, path, answer, want)
		}
	}
}

// startBackground runs binary, which listens on no port, with env, and
// waits for it to log that it opened its engine.
func startBackground(t *testing.T, binary string, env ...string) *started {
	t.Helper()
	out := &syncBuilder{}
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &started{cmd: cmd, out: out}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	// A minute: on a loaded machine a freshly linked binary is slow to
	// start, and the engine opens before the runner starts.
	deadline := time.Now().Add(time.Minute)
	for !strings.Contains(out.String(), `"msg":"runner started"`) {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not start its runner:\n%s", binary, out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	return s
}

// syncBuilder is a strings.Builder a process writes to while a test reads
// it.
type syncBuilder struct {
	mu sync.Mutex
	strings.Builder
}

func (b *syncBuilder) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Builder.Write(p)
}

func (b *syncBuilder) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Builder.String()
}

// engineDatabaseTools runs enginedb, a tool in the entrypoint module at
// dir, which requires pgx: create base name create.sql makes a database of
// its own on the Postgres at base, applies create.sql to it and prints its
// URL; drop base name drops it; grant url login role permission...
// gives the member whose login is login a role holding the permissions.
type engineDatabaseTools struct {
	dir string
}

func engineDatabaseTool(t *testing.T, dir string) engineDatabaseTools {
	t.Helper()
	tool := filepath.Join(dir, "internal", "enginedb")
	if err := os.MkdirAll(tool, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tool, "main.go"), []byte(engineDatabaseToolSource), 0o644); err != nil {
		t.Fatal(err)
	}
	return engineDatabaseTools{dir: dir}
}

func (e engineDatabaseTools) run(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("go", append([]string{"run", "./internal/enginedb"}, args...)...)
	cmd.Dir = e.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("enginedb %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// create makes a database of its own on the Postgres at base, applies
// createSQL to it, drops it when the test ends, and returns its URL.
func (e engineDatabaseTools) create(t *testing.T, base, createSQL string) string {
	t.Helper()
	name := fmt.Sprintf("engine_entrypoint_%d", time.Now().UnixNano())
	url := e.run(t, "create", base, name, createSQL)
	t.Cleanup(func() { e.run(t, "drop", base, name) })
	return url
}

// grant gives the member whose login is login the role named role, which
// holds permissions.
func (e engineDatabaseTools) grant(t *testing.T, url, login, role string, permissions ...string) {
	t.Helper()
	e.run(t, append([]string{"grant", url, login, role}, permissions...)...)
}

// engineDatabaseToolSource is enginedb's source.
const engineDatabaseToolSource = `package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
)

func main() {
	if err := run(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(command string, args []string) error {
	ctx := context.Background()
	switch command {
	case "create":
		base, name, createSQL := args[0], args[1], args[2]
		admin, err := pgx.Connect(ctx, base)
		if err != nil {
			return err
		}
		defer admin.Close(ctx)
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
			return err
		}
		u, err := url.Parse(base)
		if err != nil {
			return err
		}
		u.Path = "/" + name
		conn, err := pgx.Connect(ctx, u.String())
		if err != nil {
			return err
		}
		defer conn.Close(ctx)
		for _, ext := range []string{"pgcrypto", "citext"} {
			if _, err := conn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS "+ext); err != nil {
				return err
			}
		}
		ddl, err := os.ReadFile(createSQL)
		if err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, string(ddl)); err != nil {
			return err
		}
		fmt.Println(u.String())
		return nil
	case "drop":
		admin, err := pgx.Connect(ctx, args[0])
		if err != nil {
			return err
		}
		defer admin.Close(ctx)
		_, err = admin.Exec(ctx, "DROP DATABASE "+args[1]+" WITH (FORCE)")
		return err
	case "grant":
		conn, err := pgx.Connect(ctx, args[0])
		if err != nil {
			return err
		}
		defer conn.Close(ctx)
		login, role, permissions := args[1], args[2], args[3:]
		if _, err := conn.Exec(ctx, "INSERT INTO member_role (name, permissions) VALUES ($1, $2)", role, permissions); err != nil {
			return err
		}
		tag, err := conn.Exec(ctx, "INSERT INTO user_role_grant (user_id, role_id) SELECT m.id, r.id FROM member m, member_role r WHERE m.email = $1 AND r.name = $2", login, role)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("no member has the login %s", strings.TrimSpace(login))
		}
		return nil
	}
	return fmt.Errorf("unknown command %s", command)
}
`

// TestTheEngineReachesCloudSQLThroughASocket: in desk-cloud-stack, whose
// Staging places desk-db on Cloud SQL, desk-api's server and desk-db's
// runner link the Cloud SQL connector, and the engine opens on a socket
// cloudsql.go serves over it (D58), whose URL names the IAM user and the
// database and which carries a client's connection over the connector's
// dial (testdata/cloudsql_engine_test.go, run in the server's module with
// the fake Postgres of testdata/cloudsql_test.go). In desk-stack, which
// places nothing on Cloud SQL, neither links it. The modules come from
// the network, so the test skips when go mod tidy cannot fetch them.
func TestTheEngineReachesCloudSQLThroughASocket(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	requireEngineArchive(t, paths)
	repoRoot := t.TempDir()
	f := loadEngineFixture(t)
	f.build(t, repoRoot, paths, "desk-db", "desk-api", engineStack, engineCloudStack)
	out := filepath.Join(repoRoot, "schemas", "dist")
	for _, d := range []string{engineServer, engineRunner} {
		data, err := os.ReadFile(filepath.Join(servergen.ServerDir(out, engineCloudStack, d), servergen.CloudSQLFile))
		if err != nil || !strings.Contains(string(data), "func cloudSQLEngineSocket(") {
			t.Errorf("%s of %s serves the engine no Cloud SQL socket: %v", d, engineCloudStack, err)
		}
		if _, err := os.Stat(filepath.Join(servergen.ServerDir(out, engineStack, d), servergen.CloudSQLFile)); err == nil {
			t.Errorf("%s of %s, which no environment places on Cloud SQL, links the connector", d, engineStack)
		}
	}
	dir := servergen.ServerDir(out, engineCloudStack, engineServer)
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
	}
	goCommand(t, dir, "vet", ".")
	for _, file := range []string{"cloudsql_test.go", "cloudsql_engine_test.go"} {
		unit, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), unit, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	test := exec.Command("go", "test", "-v", "-count=1", "-run", "^TestCloudSQLEngineSocket$", ".")
	test.Dir = dir
	test.Env = append(os.Environ(), "PWD="+dir)
	if out, err := test.CombinedOutput(); err != nil || !strings.Contains(string(out), "--- PASS: TestCloudSQLEngineSocket") {
		t.Fatalf("go test in %s: %v\n%s", dir, err, out)
	}
	runnerDir := servergen.ServerDir(out, engineCloudStack, engineRunner)
	goCommand(t, runnerDir, "mod", "tidy")
	goCommand(t, runnerDir, "vet", ".")
}

// TestTheEngineDockerfileBuildsAnImageThatServes: desk-api's Dockerfile
// builds from a repository root holding the generated modules, the
// scaffold, the runtime modules, the engine's Go binding and the Cargo
// workspace beside it with the crates it reaches, and a superscalar
// checkout: its engine stage builds the engine's archive, which the server
// links (D58). The image serves /healthz, names the engine in /readyz with
// no database up, answers the engine's routes 503, and stops cleanly on
// docker stop. It needs Docker, and builds superscalar's archive and the
// engine's in Rust stages, so it runs only outside -short.
func TestTheEngineDockerfileBuildsAnImageThatServes(t *testing.T) {
	needDocker(t)
	repoRoot := dockerRepo(t)
	repo := testpaths.RepoRoot(t)
	copyTree(t, filepath.Join(repo, "runtime", "engine", "go"), filepath.Join(repoRoot, "runtime", "engine", "go"), "lib")
	for _, dir := range append([]string{"runtime/engine/rust"}, "runtime/engine-workqueue/rust", "runtime/schema/rust-ir", "runtime/schema/rust", "runtime/versiongraph/rust-engine", "runtime/versiongraph/rust") {
		copyTree(t, filepath.Join(repo, filepath.FromSlash(dir)), filepath.Join(repoRoot, filepath.FromSlash(dir)))
	}
	f := loadEngineFixture(t)
	f.build(t, repoRoot, fakePaths(repoRoot), engineOrder...)

	image := fmt.Sprintf("superschematic-servergen-test:%d-engine", time.Now().UnixNano())
	dockerBuild(t, repoRoot, image, "-f", "schemas/dist/server/"+engineStack+"/"+engineServer+"/Dockerfile")
	container := docker(t, "run", "-d", "-p", "127.0.0.1::8080",
		"-e", "DESK_DB_DATABASE_URL=postgres://desk@127.0.0.1:9/desk_db?connect_timeout=1&sslmode=disable",
		"-e", "DESK_API_CALLERS_ISSUERS=0", image)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", container).Run() })
	address := docker(t, "port", container, "8080/tcp")
	if i := strings.LastIndex(address, "\n"); i >= 0 {
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
