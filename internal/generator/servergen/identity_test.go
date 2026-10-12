package servergen_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
)

const (
	// identityRoot holds the identity fixture (D50): users-db declares
	// the user model; users-api, public, serves the session routes and a
	// route of its own over it, and declares a job, PurgeSessions;
	// users-admin, not public, the administration routes; and users-stack
	// serves both from one server, Accounts, on the local target. Beside
	// them, users-ts-api serves the session routes and its own route on a
	// TypeScript server (D51), its default one in users-ts-stack.
	identityRoot = "testdata/identity"

	// identityStack is the fixture's stack and identityServer its server;
	// identityJob is the deployable of users-api's job.
	identityStack  = "users-stack"
	identityServer = "Accounts"
	identityJob    = "users-api-purge-sessions"

	// identityTSStack is the stack of the TypeScript server, identityTSAPI,
	// named after the API it serves.
	identityTSStack = "users-ts-stack"
	identityTSAPI   = "users-ts-api"

	// identityPortalStack's one TypeScript server, identityPortalServer,
	// serves users-ts-api beside users-ts-notes, a second API over
	// users-db, and users-ts-ping, an API over no user model.
	identityPortalStack  = "users-ts-portal"
	identityPortalServer = "Web"

	// identityDatabaseEnv names the Postgres the entrypoint also serves
	// on, as the identity runtime's store tests read it.
	identityDatabaseEnv = "SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL"
)

// identityOrder is the order a build-all builds the identity fixture's Go
// stack in, and identityTSOrder its TypeScript stack.
var (
	identityOrder       = []string{"users-db", "users-api", "users-admin", identityStack}
	identityTSOrder     = []string{"users-db", identityTSAPI, identityTSStack}
	identityPortalOrder = []string{"users-db", identityTSAPI, "users-ts-notes", "users-ts-ping", identityPortalStack}
)

// loadIdentityFixture loads the identity fixture with the core registry,
// whose local target the stack's environment runs on.
func loadIdentityFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming())
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	names := append(slices.Clone(identityOrder), identityTSOrder[1:]...)
	names = append(names, "users-ts-notes", "users-ts-ping", identityPortalStack)
	for _, name := range names {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(identityRoot, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	return f
}

// TestIdentityEntrypointGolden: a server whose APIs authenticate with the
// identity runtime builds one identity store over their database's pool,
// from its Go types' descriptor, and each API's identity service from its
// identity config field, which identity.go reads; neither API's
// implementation writes an auth middleware. The local environment binds
// each identity config field to the local platform's config, a cookie
// without Secure. users-api's job, which serves no request, builds no
// identity store or service, has no identity.go and binds no identity
// config field (D52). Regenerate with:
//
//	go test ./internal/generator/servergen -run TestIdentityEntrypointGolden -update
func TestIdentityEntrypointGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadIdentityFixture(t)
	f.build(t, repoRoot, fakePaths(repoRoot), identityOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")

	files := map[string]string{}
	for _, file := range []string{servergen.MainFile, servergen.IdentityFile, servergen.ModFile} {
		files[filepath.Join("server", identityStack, identityServer, file)] = filepath.Join(servergen.ServerDir(out, identityStack, identityServer), file)
	}
	for _, file := range []string{servergen.MainFile, servergen.ModFile} {
		files[filepath.Join("server", identityStack, identityJob, file)] = filepath.Join(servergen.ServerDir(out, identityStack, identityJob), file)
	}
	for _, service := range []string{"users-api", "users-admin"} {
		files[filepath.Join("go", service, apigen.ImplementationFile)] = filepath.Join(naming.Default().GoImplementationDir(repoRoot, service), apigen.ImplementationFile)
	}
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		golden := filepath.Join(goldenRoot, "identity", rel)
		got, err := os.ReadFile(files[rel])
		if err != nil {
			t.Fatal(err)
		}
		if *update {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%s has no golden (run with -update to write it): %v", rel, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from %s; run with -update and review the diff", rel, golden)
		}
	}
	for _, service := range []string{"users-api", "users-admin"} {
		impl, err := os.ReadFile(files[filepath.Join("go", service, apigen.ImplementationFile)])
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(impl), "AuthMiddleware") {
			t.Errorf("the implementation scaffold of %s writes an auth middleware:\n%s", service, impl)
		}
	}
	if _, err := os.Stat(filepath.Join(servergen.ServerDir(out, identityStack, identityJob), servergen.IdentityFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the job %s has %s: %v", identityJob, servergen.IdentityFile, err)
	}
	job, err := os.ReadFile(files[filepath.Join("server", identityStack, identityJob, servergen.MainFile)])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(job), "identity") {
		t.Errorf("the job %s builds the identity runtime:\n%s", identityJob, job)
	}

	data, err := os.ReadFile(filepath.Join(out, "stack", identityStack, "Local", "environment.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env ir.ResolvedEnvironment
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	bound := map[string]*ir.Binding{}
	for _, d := range env.Deployables {
		if d.Name == identityJob {
			for _, b := range d.Bindings {
				if b.IdentityOf != "" {
					t.Errorf("the job %s binds the identity config field %s", identityJob, b.Field)
				}
			}
		}
		if d.Name != identityServer {
			continue
		}
		for _, b := range d.Bindings {
			bound[b.Field] = b
		}
	}
	for field, api := range map[string]string{"USERS_API_IDENTITY": "users-api", "USERS_ADMIN_IDENTITY": "users-admin"} {
		b := bound[field]
		if b == nil || b.IdentityOf != api || b.Source != ir.BindingLiteral || !b.Default || b.Value != `{"cookie":{"secure":false}}` {
			t.Errorf("Accounts binds %s as %+v, want the local platform's identity config for %s", field, b, api)
		}
	}
}

// TestIdentityEntrypointServes: the entrypoint builds and vets, starts with
// no database up, and answers what needs no database: a protected route
// refuses a request without a session, and a trusted origin's preflight
// reaches the API that registers the route it asks about, whose CORS
// middleware answers it. With the Postgres identityDatabaseEnv names, it
// serves users-db's user model: a user registers through users-api and
// signs in, the session authenticates the project's route, which the
// scaffold answers, and me, and users-admin, which serves the
// administration routes over the same store, refuses the user a
// permission they do not hold.
func TestIdentityEntrypointServes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	repoRoot := t.TempDir()
	f := loadIdentityFixture(t)
	f.build(t, repoRoot, paths, identityOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	dir := servergen.ServerDir(out, identityStack, identityServer)
	goCommand(t, dir, "mod", "tidy")
	goCommand(t, dir, "vet", ".")
	binary := filepath.Join(t.TempDir(), identityServer)
	goCommand(t, dir, "build", "-o", binary, ".")
	// The job of users-api builds on its own, without the identity runtime.
	jobDir := servergen.ServerDir(out, identityStack, identityJob)
	goCommand(t, jobDir, "mod", "tidy")
	goCommand(t, jobDir, "vet", ".")

	identityConfig := `{"trustedOrigins": ["https://app.example.com"], "cookie": {"secure": false}, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}`
	identity := []string{"USERS_API_IDENTITY=" + identityConfig, "USERS_ADMIN_IDENTITY=" + identityConfig}

	offline := start(t, binary, append([]string{"USERS_DB_DATABASE_URL=postgres://users@127.0.0.1:9/users_db?connect_timeout=1&sslmode=disable"}, identity...)...)
	offline.expect(t, http.MethodGet, "/api/greeting", http.StatusUnauthorized)
	offline.expectPreflight(t, "/api/auth/admin/users", http.MethodGet)
	offline.expectPreflight(t, "/api/auth/login", http.MethodPost)
	offline.stop(t)

	// An identity config the runtime refuses stops the server, naming the
	// field.
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), "PORT="+freePort(t), "USERS_DB_DATABASE_URL=postgres://users@127.0.0.1:9/users_db?connect_timeout=1&sslmode=disable", `USERS_API_IDENTITY={"cookie": {"sameSite": "Loose"}}`)
	if refused, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(refused), "USERS_API_IDENTITY: identity: config: cookie.sameSite") {
		t.Errorf("the server on a refused identity config = %v:\n%s", err, refused)
	}

	postgres := os.Getenv(identityDatabaseEnv)
	if postgres == "" {
		t.Logf("%s is unset: the entrypoint serves no database", identityDatabaseEnv)
		return
	}
	databaseURL := identitySchema(t, dir, postgres, filepath.Join(generator.SQLDir(out, "users-db"), "create.sql"))
	online := start(t, binary, append([]string{"USERS_DB_DATABASE_URL=" + databaseURL}, identity...)...)
	online.expect(t, http.MethodGet, "/readyz", http.StatusOK)
	online.send(t, http.MethodPost, "/api/auth/register", `{"login": "ada@example.com", "name": "Ada", "password": "ada's password"}`, http.StatusOK, `"token"`)
	token := online.login(t, "ada@example.com", "ada's password")
	online.authorized(t, token, http.MethodGet, "/api/greeting", http.StatusNotImplemented, "Greeting.Greet")
	online.authorized(t, token, http.MethodGet, "/api/auth/me", http.StatusOK, `"login":"ada@example.com"`)
	online.authorized(t, token, http.MethodGet, "/api/auth/admin/users", http.StatusForbidden, `"code":"forbidden"`)
	online.authorized(t, token, http.MethodPost, "/api/auth/logout", http.StatusOK, `"data":true`)
	online.authorized(t, token, http.MethodGet, "/api/auth/me", http.StatusUnauthorized)
	online.stop(t)
}

// expectPreflight sends a trusted origin's preflight for method on path
// and checks the credentialed CORS answer.
func (s *started) expectPreflight(t *testing.T, path, method string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodOptions, s.base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", method)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != "https://app.example.com" || resp.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("the preflight of %s %s answered %d with %v", method, path, resp.StatusCode, resp.Header)
	}
}

// login signs in with a bearer session and returns its token.
func (s *started) login(t *testing.T, login, password string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"login": login, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(s.base+"/api/auth/login", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var answer struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	data, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(data, &answer); err != nil || resp.StatusCode != http.StatusOK || answer.Data.Token == "" {
		t.Fatalf("login answered %d: %s", resp.StatusCode, data)
	}
	return answer.Data.Token
}

// authorized is send with the bearer token and no body.
func (s *started) authorized(t *testing.T, token, method, path string, status int, contains ...string) {
	t.Helper()
	req, err := http.NewRequest(method, s.base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
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
}

// identitySchema creates a schema of its own in the Postgres at base,
// applies createSQL to it, drops it when the test ends, and returns a
// connection string whose search path is it. It runs identitySchemaTool
// from the entrypoint module at dir, which requires pgx.
func identitySchema(t *testing.T, dir, base, createSQL string) string {
	t.Helper()
	tool := filepath.Join(dir, "internal", "identityschema")
	if err := os.MkdirAll(tool, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tool, "main.go"), []byte(identitySchemaTool), 0o644); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("identity_entrypoint_%d", time.Now().UnixNano())
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("go", append([]string{"run", "./internal/identityschema"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("identityschema %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	databaseURL := run("create", base, schema, createSQL)
	t.Cleanup(func() { run("drop", base, schema) })
	return databaseURL
}

// identitySchemaTool creates or drops a schema of the identity fixture's
// tables: create base schema create.sql prints a connection string whose
// search path is the new schema; drop base schema drops it.
const identitySchemaTool = `package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
)

func main() {
	if err := run(os.Args[1], os.Args[2], os.Args[3], os.Args[4:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(command, base, schema string, rest []string) error {
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		return err
	}
	defer admin.Close(ctx)
	if command == "drop" {
		_, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		return err
	}
	for _, ext := range []string{"pgcrypto", "citext"} {
		if _, err := admin.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS "+ext+" SCHEMA public"); err != nil && !strings.Contains(err.Error(), "23505") {
			return err
		}
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		return err
	}
	u, err := url.Parse(base)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	ddl, err := os.ReadFile(rest[0])
	if err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, string(ddl)); err != nil {
		return err
	}
	fmt.Println(u.String())
	return nil
}
`

// TestTypeScriptIdentityEntrypointGolden: a TypeScript server whose API
// authenticates with the identity runtime (D50, D51) builds an identity
// store over its database's pool, from the descriptor the database's
// TypeScript types export, and the API's identity service from its
// identity config field, which it passes buildRouter in place of an
// authenticator. Neither the API's Deps nor its scaffolded implementation
// has an authenticate. The local environment binds the identity config
// field to the local platform's config. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestTypeScriptIdentityEntrypointGolden -update
func TestTypeScriptIdentityEntrypointGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadIdentityFixture(t)
	f.build(t, repoRoot, tsFakePaths(repoRoot), identityTSOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	dir := servergen.ServerDir(out, identityTSStack, identityTSAPI)
	impl := naming.Default().TypeScriptImplementationDir(repoRoot, identityTSAPI)
	files := map[string]string{
		filepath.Join("typescript", "server", identityTSStack, identityTSAPI, servergen.TypeScriptMainFile):    filepath.Join(dir, servergen.TypeScriptMainFile),
		filepath.Join("typescript", "server", identityTSStack, identityTSAPI, servergen.TypeScriptPackageFile): filepath.Join(dir, servergen.TypeScriptPackageFile),
		filepath.Join("typescript", identityTSAPI, "index.ts"):                                                 filepath.Join(impl, "index.ts"),
	}
	compareGoldens(t, filepath.Join(goldenRoot, "identity"), files)

	deps, err := os.ReadFile(filepath.Join(generator.APIDir(out, identityTSAPI), "deps.ts"))
	if err != nil {
		t.Fatal(err)
	}
	scaffold, err := os.ReadFile(files[filepath.Join("typescript", identityTSAPI, "index.ts")])
	if err != nil {
		t.Fatal(err)
	}
	for name, code := range map[string][]byte{"deps.ts": deps, "the scaffold": scaffold} {
		if strings.Contains(string(code), "authenticate") || strings.Contains(string(code), "AuthenticatorFactory") {
			t.Errorf("%s of %s has an authenticator:\n%s", name, identityTSAPI, code)
		}
	}

	data, err := os.ReadFile(filepath.Join(out, "stack", identityTSStack, "Local", "environment.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env ir.ResolvedEnvironment
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	var binding *ir.Binding
	for _, d := range env.Deployables {
		for _, b := range d.Bindings {
			if d.Name == identityTSAPI && b.Field == ir.IdentityConfigField(identityTSAPI) {
				binding = b
			}
		}
	}
	if binding == nil || binding.IdentityOf != identityTSAPI || binding.Source != ir.BindingLiteral || binding.Value != `{"cookie":{"secure":false}}` {
		t.Errorf("%s binds %s as %+v, want the local platform's identity config", identityTSAPI, ir.IdentityConfigField(identityTSAPI), binding)
	}
}

// TestATypeScriptIdentityServerNeedsItsAuthDbTypes: a TypeScript server's
// identity store reads the descriptor the authDb's TypeScript types
// export, so the stack's build refuses one whose authDb generates none,
// saying what to enable.
func TestATypeScriptIdentityServerNeedsItsAuthDbTypes(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadIdentityFixture(t)
	cfg := *f.configs["users-db"]
	cfg.Outputs = map[string]any{"types": map[string]any{"go": map[string]any{"enabled": true}}}
	f.configs["users-db"] = &cfg
	options := f.options(repoRoot, tsFakePaths(repoRoot))
	for _, name := range identityTSOrder {
		_, err := generator.Run(f.schemas[name], f.configs[name], options)
		if name != identityTSStack {
			if err != nil {
				t.Fatalf("build %s: %v", name, err)
			}
			continue
		}
		if want := "enable outputs.types.typescript in users-db's config"; err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("build %s = %v, want it refused saying %q", name, err, want)
		}
	}
}

// TestTypeScriptIdentityEntrypointServes: the TypeScript server's package
// installs in the output root's workspace, main.ts type-checks, and Bun
// runs it with no database up: a protected route refuses a request
// without a session, a trusted origin's preflight is answered, and an
// identity config the runtime refuses stops the server, naming the field.
// With the Postgres identityDatabaseEnv names, it serves users-db's user
// model: a user registers and signs in, the session authenticates the
// project's route, which the scaffold answers, and me, and logout ends it.
func TestTypeScriptIdentityEntrypointServes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	installTypeScriptRuntime(t, bun, paths)
	repoRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := loadIdentityFixture(t)
	f.build(t, repoRoot, paths, identityTSOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	install := exec.Command(bun, "install")
	install.Dir = out
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	dir := servergen.ServerDir(out, identityTSStack, identityTSAPI)
	tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
	for _, pkg := range []string{dir, naming.Default().TypeScriptImplementationDir(repoRoot, identityTSAPI)} {
		cmd := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
		cmd.Dir = pkg
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("tsc in %s: %v\n%s", pkg, err, output)
		}
	}

	field := ir.IdentityConfigField(identityTSAPI)
	identityConfig := field + `={"trustedOrigins": ["https://app.example.com"], "cookie": {"secure": false}, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}`
	unreachable := "USERS_DB_DATABASE_URL=postgres://users@127.0.0.1:9/users_db?connect_timeout=1&sslmode=disable"
	offline := bunServer(t, bun, dir, unreachable, identityConfig)
	offline.expect(t, http.MethodGet, "/api/greeting", http.StatusUnauthorized)
	offline.expectPreflight(t, "/api/auth/login", http.MethodPost)
	offline.stop(t)

	cmd := exec.Command(bun, servergen.TypeScriptMainFile)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PORT="+freePort(t), unreachable, field+`={"cookie": {"sameSite": "Loose"}}`)
	if refused, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(refused), field+": identity: config: cookie.sameSite") {
		t.Errorf("the server on a refused identity config = %v:\n%s", err, refused)
	}

	postgres := os.Getenv(identityDatabaseEnv)
	if postgres == "" {
		t.Logf("%s is unset: the entrypoint serves no database", identityDatabaseEnv)
		return
	}
	databaseURL := identitySchemaTS(t, bun, dir, postgres, filepath.Join(generator.SQLDir(out, "users-db"), "create.sql"))
	online := bunServer(t, bun, dir, "USERS_DB_DATABASE_URL="+databaseURL, identityConfig)
	online.expect(t, http.MethodGet, "/readyz", http.StatusOK)
	online.send(t, http.MethodPost, "/api/auth/register", `{"login": "ada@example.com", "name": "Ada", "password": "ada's password"}`, http.StatusOK, `"token"`)
	token := online.login(t, "ada@example.com", "ada's password")
	online.authorized(t, token, http.MethodGet, "/api/greeting", http.StatusNotImplemented)
	online.authorized(t, token, http.MethodGet, "/api/auth/me", http.StatusOK, `"login":"ada@example.com"`)
	online.authorized(t, token, http.MethodPost, "/api/auth/logout", http.StatusOK, `"data":true`)
	online.authorized(t, token, http.MethodGet, "/api/auth/me", http.StatusUnauthorized)
	online.stop(t)
}

// TestTypeScriptIdentityCorsStaysWithItsAPI: a TypeScript server that
// serves several APIs gives each identity API's trusted origins
// credentialed CORS on that API's routes alone, as the Go server's
// dispatch does. users-ts-api trusts one origin and users-ts-notes
// another; users-ts-ping, over no user model, answers neither. A
// preflight goes to the API that registers the method it asks for on the
// path: users-ts-notes registers POST where users-ts-api registers GET.
func TestTypeScriptIdentityCorsStaysWithItsAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	installTypeScriptRuntime(t, bun, paths)
	repoRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := loadIdentityFixture(t)
	f.build(t, repoRoot, paths, identityPortalOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	install := exec.Command(bun, "install")
	install.Dir = out
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	dir := servergen.ServerDir(out, identityPortalStack, identityPortalServer)
	tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
	cmd := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tsc in %s: %v\n%s", dir, err, output)
	}

	const a, b = "https://a.example.com", "https://b.example.com"
	trusting := func(api, origin string) string {
		return ir.IdentityConfigField(api) + `={"trustedOrigins": ["` + origin + `"], "cookie": {"secure": false}}`
	}
	server := bunServer(t, bun, dir,
		"USERS_DB_DATABASE_URL=postgres://users@127.0.0.1:9/users_db?connect_timeout=1&sslmode=disable",
		trusting(identityTSAPI, a), trusting("users-ts-notes", b))
	defer server.stop(t)
	// answer sends method to path from origin, a preflight when requested
	// names a method, and returns the status and the CORS it got.
	answer := func(method, path, origin, requested string) string {
		t.Helper()
		req, err := http.NewRequest(method, server.base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", origin)
		if requested != "" {
			req.Header.Set("Access-Control-Request-Method", requested)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return fmt.Sprintf("%d %s %s", resp.StatusCode, resp.Header.Get("Access-Control-Allow-Origin"), resp.Header.Get("Access-Control-Allow-Credentials"))
	}
	for _, c := range []struct{ method, path, origin, requested, want string }{
		// Each API's routes answer its own trusted origin, before the
		// identity runtime refuses a request without a session.
		{"GET", "/api/greeting", a, "", "401 " + a + " true"},
		{"GET", "/api/notes", b, "", "401 " + b + " true"},
		{"POST", "/api/greeting", b, "", "401 " + b + " true"},
		// And no other API's.
		{"GET", "/api/notes", a, "", "401  "},
		{"POST", "/api/greeting", a, "", "401  "},
		{"GET", "/api/greeting", b, "", "401  "},
		{"GET", "/api/ping", a, "", "501  "},
		{"GET", "/api/ping", b, "", "501  "},
		// A preflight is answered by the API that registers the method.
		{"OPTIONS", "/api/greeting", a, "GET", "204 " + a + " true"},
		{"OPTIONS", "/api/greeting", b, "POST", "204 " + b + " true"},
		{"OPTIONS", "/api/notes", b, "GET", "204 " + b + " true"},
		{"OPTIONS", "/api/auth/login", a, "POST", "204 " + a + " true"},
		{"OPTIONS", "/api/greeting", a, "POST", "404  "},
		{"OPTIONS", "/api/greeting", b, "GET", "404  "},
		{"OPTIONS", "/api/notes", a, "GET", "404  "},
		{"OPTIONS", "/api/auth/login", b, "POST", "404  "},
		{"OPTIONS", "/api/ping", a, "GET", "404  "},
	} {
		if got := answer(c.method, c.path, c.origin, c.requested); got != c.want {
			t.Errorf("%s %s from %s (asking for %q) answered %q, want %q", c.method, c.path, c.origin, c.requested, got, c.want)
		}
	}
}

// identitySchemaTS is identitySchema through pg under Bun, from the
// TypeScript server's package at dir, which depends on pg: it creates a
// schema of its own in the Postgres at base, applies createSQL to it, drops
// it when the test ends, and returns a connection string whose connections
// find it first on their search path.
func identitySchemaTS(t *testing.T, bun, dir, base, createSQL string) string {
	t.Helper()
	tool := filepath.Join(t.TempDir(), "identity-schema.ts")
	if err := os.WriteFile(tool, []byte(identitySchemaScript), 0o644); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("identity_ts_entrypoint_%d", time.Now().UnixNano())
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bun, append([]string{"run", tool}, args...)...)
		cmd.Dir = dir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("identity-schema.ts %s: %v\n%s", strings.Join(args, " "), err, output)
		}
		return strings.TrimSpace(string(output))
	}
	databaseURL := run("create", base, schema, createSQL)
	t.Cleanup(func() { run("drop", base, schema) })
	return databaseURL
}

// identitySchemaScript creates or drops a schema of the identity fixture's
// tables with pg, which it imports from the package it runs in: create
// base schema create.sql prints a connection string whose search path
// starts with the new schema; drop base schema drops it.
const identitySchemaScript = `import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { join } from 'node:path';

const pg = createRequire(join(process.cwd(), 'package.json'))('pg');
const [command, base, schema, ddl] = process.argv.slice(2);
const admin = new pg.Client({ connectionString: base });
await admin.connect();
try {
  if (command === 'drop') {
    await admin.query('DROP SCHEMA ' + schema + ' CASCADE');
  } else {
    for (const extension of ['pgcrypto', 'citext']) {
      try {
        await admin.query('CREATE EXTENSION IF NOT EXISTS ' + extension + ' SCHEMA public');
      } catch (error) {
        if (error.code !== '23505') throw error;
      }
    }
    await admin.query('CREATE SCHEMA ' + schema);
    const url = new URL(base);
    url.searchParams.set('options', '-c search_path=' + schema + ',public');
    const client = new pg.Client({ connectionString: url.toString() });
    await client.connect();
    try {
      await client.query(readFileSync(ddl, 'utf8'));
    } finally {
      await client.end();
    }
    console.log(url.toString());
  }
} finally {
  await admin.end();
}
`
