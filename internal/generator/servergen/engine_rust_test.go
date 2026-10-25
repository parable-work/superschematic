package servergen_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestRustEngineEntrypointGolden: desk-rs-api's Rust server opens the
// engine of desk-db in the background (engine.rs), deploys its engine
// document before /readyz reports it ready, mounts its routes beside
// desk-rs-api's at /engine/desk-db behind the identity runtime's session
// and the service authenticator, and holds it in desk-rs-api's Deps;
// desk-rs-api's job opens it for its Deps and deploys nothing; desk-db's
// runner, in Rust, opens the engine and runs its runner as itself, holding
// each engine type (D58). Each crate depends on the engine's crate and the
// core's behaviors by path, and each image's context holds the engine's
// workspace. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestRustEngineEntrypointGolden -update
func TestRustEngineEntrypointGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadEngineFixture(t)
	f.build(t, repoRoot, rustFakePaths(repoRoot), rustEngineOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	files := map[string]string{}
	for _, d := range []struct {
		name  string
		files []string
	}{
		{rustEngineServer, []string{servergen.RustManifestFile, servergen.RustMainFile, servergen.RustEngineFile, servergen.DockerFile, servergen.DockerIgnoreFile}},
		{engineRunner, []string{servergen.RustManifestFile, servergen.RustMainFile, servergen.RustEngineFile, servergen.DockerFile}},
		{rustEngineJob, []string{servergen.RustManifestFile, servergen.RustMainFile, servergen.RustEngineFile}},
	} {
		for _, file := range d.files {
			files[filepath.Join("server", rustEngineStack, d.name, filepath.FromSlash(file))] = filepath.Join(servergen.ServerDir(out, rustEngineStack, d.name), filepath.FromSlash(file))
		}
	}
	for _, file := range []string{"Cargo.toml", "src/deps.rs"} {
		files[filepath.Join("api", "desk-rs-api", filepath.FromSlash(file))] = filepath.Join(generator.APIDir(out, "desk-rs-api"), filepath.FromSlash(file))
	}
	compareGoldens(t, filepath.Join(goldenRoot, "engine"), files)
}

// TestRustEngineEntrypointServes: desk-rs-api's Rust server, its job and
// desk-db's Rust runner build in the output root's workspace, frozen to
// the lockfile the stack's build wrote, with the engine's crate linked in,
// pass clippy, and are laid out as rustfmt lays them out (D58). The server
// starts with no database up and answers what needs none: /readyz names
// the engine, its routes under /engine/desk-db answer 503 unavailable
// until it opens, and a trusted origin's preflight of one gets the
// identity runtime's CORS. With the Postgres engineDatabaseEnv names, it
// opens the engine on desk-db's database and deploys its document, and
// answers as desk-api's Go server does (TestEngineEntrypointServes): a
// member refused a write their roles lack, then granted Account and
// Ticket, creates an account, reads it back and opens a ticket; a service
// standing in is refused by the permission policy, and a forged service
// credential is 401 service_unauthorized. The runner, started beside it,
// fires the ticket's deadline, which escalates it as the runner. Both stop
// cleanly on SIGTERM.
func TestRustEngineEntrypointServes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		skipEngineServers(t, "cargo not available; skipping the Rust build")
	}
	paths := testpaths.Local(t)
	repoRoot := testpaths.TempDir(t)
	f := loadEngineFixture(t)
	f.build(t, repoRoot, paths, rustEngineOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	if _, err := os.Stat(filepath.Join(out, servergen.RustLockfileName)); err != nil {
		t.Fatalf("the stack's build wrote no lockfile: %v", err)
	}
	target := rustTargetDir(t)
	cargoCommand(t, cargo, out, target, "clippy", "--locked", "--workspace", "--quiet", "--", "-D", "warnings")
	cargoCommand(t, cargo, out, target, "build", "--locked", "--workspace", "--quiet")
	bin := filepath.Join(target, "debug")
	if rustfmt, err := exec.LookPath("rustfmt"); err == nil {
		for _, d := range []string{rustEngineServer, engineRunner, rustEngineJob} {
			for _, file := range []string{servergen.RustMainFile, servergen.RustEngineFile} {
				source := filepath.Join(servergen.ServerDir(out, rustEngineStack, d), filepath.FromSlash(file))
				if diff, err := exec.Command(rustfmt, "--edition", "2021", "--check", source).CombinedOutput(); err != nil {
					t.Errorf("rustfmt lays out %s's %s otherwise: %v\n%s", d, file, err, diff)
				}
			}
		}
	}

	identityConfig := `{"trustedOrigins": ["https://app.example.com"], "cookie": {"secure": false}, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}`
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	env := append([]string{"DESK_RS_API_IDENTITY=" + identityConfig}, opsCallersVariables(t, rustEngineServer, public, "ops-key")...)
	server := filepath.Join(bin, rustEngineServer)

	offline := start(t, server, append([]string{"DESK_DB_DATABASE_URL=postgres://desk@127.0.0.1:9/desk_db?connect_timeout=1&sslmode=disable"}, env...)...)
	offline.expect(t, http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"desk-db engine"`)
	offline.expect(t, http.MethodGet, "/engine/desk-db/namespaces/desk-db/schemas", http.StatusServiceUnavailable, `"code":"unavailable"`, "The engine is not available")
	offline.expectPreflight(t, "/engine/desk-db/namespaces/desk-db/schemas/Ticket/instances", http.MethodPost)
	offline.expect(t, http.MethodGet, "/api/summary", http.StatusUnauthorized)
	// Each attempt to open the engine runs on the blocking pool, and the
	// first one's failure is logged once it gives up on the database.
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
	tool := engineDatabaseTool(t, pgxModule(t))
	databaseURL := tool.create(t, postgres, filepath.Join(generator.SQLDir(out, "desk-db"), "create.sql"))
	online := start(t, server, append([]string{"DESK_DB_DATABASE_URL=" + databaseURL}, env...)...)
	online.waitReady(t)
	if !strings.Contains(online.out.String(), `"msg":"engine schema deployed"`) {
		t.Errorf("the server did not log its deploy:\n%s", online.out.String())
	}
	online.send(t, http.MethodPost, "/api/auth/register", `{"login": "ada@example.com", "name": "Ada", "password": "ada's password"}`, http.StatusOK, `"token"`)
	token := online.login(t, "ada@example.com", "ada's password")
	const accounts = "/engine/desk-db/namespaces/desk-db/schemas/Account/instances"
	const tickets = "/engine/desk-db/namespaces/desk-db/schemas/Ticket/instances"
	online.engine(t, token, http.MethodPost, accounts, `{"data": {"name": "Acme"}}`, http.StatusForbidden, `"code":"forbidden"`)
	tool.grant(t, databaseURL, "ada@example.com", "desk", "Account", "Ticket")
	account := online.engine(t, token, http.MethodPost, accounts, `{"id": "acme", "data": {"name": "Acme"}}`, http.StatusCreated, `"id":"acme"`)
	if !strings.Contains(account, `"createdBy":"`) || strings.Contains(account, `"createdBy":"`+engineRunner+`"`) {
		t.Errorf("the account was created as %s", account)
	}
	online.engine(t, token, http.MethodGet, accounts+"/acme", "", http.StatusOK, `"name":"Acme"`, `"Comments":{"messages":0,"threads":{"open":0,"resolved":0}}`)
	online.engine(t, token, http.MethodPost, tickets, `{"id": "t1", "data": {"title": "The printer is out of toner", "contact": {"email": "bob@example.com"}}, "behaviors": {"Links": {"account": "acme"}}}`,
		http.StatusCreated, `"status":"open"`, `"respond":{"dueAt":`)
	online.service(t, signEdgeToken(t, key, "ops-key", "ops", rustEngineServer), http.MethodGet, accounts+"/acme", http.StatusForbidden, `"code":"forbidden"`, "service:ops")
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	online.service(t, signEdgeToken(t, other, "ops-key", "ops", rustEngineServer), http.MethodGet, accounts+"/acme", http.StatusUnauthorized, `"code":"service_unauthorized"`)

	runner := startBackground(t, filepath.Join(bin, engineRunner), "DESK_DB_DATABASE_URL="+databaseURL)
	deadline := time.Now().Add(30 * time.Second)
	for {
		ticket := online.engine(t, token, http.MethodGet, tickets+"/t1", "", http.StatusOK)
		if strings.Contains(ticket, `"status":"escalated"`) {
			for _, want := range []string{`"fired":true`, `"outcome":"moved"`, `"updatedBy":"` + engineRunner + `"`} {
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

// TestTheRustEngineDockerfileBuildsAnImageThatServes: desk-rs-api's
// Dockerfile builds from the context a deploy writes out of a repository
// root holding the generated crates, the scaffolds, the workspace's
// lockfile and copies of the HTTP runtime, the schema runtime,
// superscalar's crate and the engine's Cargo workspace with the crates it
// reaches, which the image's context takes in (D58). The image serves
// /healthz, names the engine in /readyz with no database up, answers the
// engine's routes 503, and stops cleanly on docker stop. It needs Docker
// and the network, and runs only outside -short.
func TestTheRustEngineDockerfileBuildsAnImageThatServes(t *testing.T) {
	needDocker(t)
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo not available: the stack's build writes the lockfile with it")
	}
	local := testpaths.Local(t)
	repo := testpaths.RepoRoot(t)
	repoRoot := testpaths.TempDir(t)
	paths := rustFakePaths(repoRoot)
	copyTree(t, local.HTTPRuntimeRust, paths.HTTPRuntimeRust)
	copyTree(t, local.SchemaRuntimeRust, paths.SchemaRuntimeRust)
	copyTree(t, local.ScalarRust, paths.ScalarRust)
	workspace, err := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(local.ScalarRust)), "Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(filepath.Dir(paths.ScalarRust)), "Cargo.toml"), workspace, 0o644); err != nil {
		t.Fatal(err)
	}
	// The engine's workspace reaches the crates of superscalar beside its
	// core, as the checkout lays them out.
	copyTree(t, filepath.Join(repo, "third_party", "superscalar", "crates"), filepath.Join(repoRoot, "third_party", "superscalar", "crates"))
	for _, dir := range []string{"runtime/engine/rust", "runtime/engine-workqueue/rust", "runtime/engine-commerce/rust", "runtime/schema/rust-ir", "runtime/versiongraph/rust-engine", "runtime/versiongraph/rust"} {
		copyTree(t, filepath.Join(repo, filepath.FromSlash(dir)), filepath.Join(repoRoot, filepath.FromSlash(dir)))
	}
	f := loadEngineFixture(t)
	f.build(t, repoRoot, paths, rustEngineOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	if _, err := os.Stat(filepath.Join(out, servergen.RustLockfileName)); err != nil {
		t.Fatalf("the stack's build wrote no lockfile: %v", err)
	}
	image := buildRustImage(t, repoRoot, servergen.ServerDir(out, rustEngineStack, rustEngineServer))

	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "-d", "-p", "127.0.0.1::8080",
		"-e", "DESK_DB_DATABASE_URL=postgres://desk@127.0.0.1:9/desk_db?connect_timeout=1&sslmode=disable",
		"-e", `DESK_RS_API_IDENTITY={"cookie": {"secure": false}}`}
	for _, v := range opsCallersVariables(t, rustEngineServer, public, "ops-key") {
		args = append(args, "-e", v)
	}
	container := docker(t, append(args, image)...)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", container).Run() })
	s := &started{base: "http://" + firstLine(docker(t, "port", container, "8080/tcp")), out: &strings.Builder{}}
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
	if logs := docker(t, "logs", container); !strings.Contains(logs, `"msg":"stopped"`) {
		t.Errorf("the container did not log its graceful stop:\n%s", logs)
	}
}
