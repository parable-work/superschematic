package servergen_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/projectengine"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// TestEngineProjectArchiveGolden: queue-db's Job composes the work queue's
// Lease and Queue, which the core's engine archive leaves out, so queue-db's
// build writes the project's engine crate, engine/rust/queue-db, with the
// work queue's crate and no extension's (D58). queue-api's server and
// queue-db's runner are not refused: the engine stage of each checkout
// Dockerfile builds the archive of the crate's ffi member in the core's
// place, its lock file seeded from the engine workspace's, and the build
// context holds the crate. Each module's projectengine.LinkFile names the
// crate's ffi member and the engine workspace's lock file, from which the
// local target builds the archive. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestEngineProjectArchiveGolden -update
func TestEngineProjectArchiveGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadEngineFixture(t)
	f.build(t, repoRoot, fakePaths(repoRoot), queueOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	crate := generator.EngineDir(out, generator.LangRust, "queue-db")
	manifest, err := os.ReadFile(filepath.Join(crate, projectengine.ManifestFile))
	if err != nil {
		t.Fatalf("queue-db's build wrote no engine crate: %v", err)
	}
	if !strings.Contains(string(manifest), "\nsuperschematic-engine-workqueue = { path = ") {
		t.Errorf("the crate depends on no work queue crate:\n%s", manifest)
	}
	if _, err := os.Stat(filepath.Join(crate, filepath.FromSlash(projectengine.FFIManifestFile))); err != nil {
		t.Errorf("the crate has no ffi member: %v", err)
	}
	files := map[string]string{}
	for _, d := range []string{queueServer, queueRunner} {
		for _, file := range []string{servergen.DockerFile, servergen.DockerIgnoreFile, projectengine.LinkFile} {
			files[filepath.Join("server", queueStack, d, file)] = filepath.Join(servergen.ServerDir(out, queueStack, d), file)
		}
	}
	compareGoldens(t, filepath.Join(goldenRoot, "engine"), files)
}

// ratingWithoutACrate registers acme.Rating as an extension's behavior
// whose registration names no Rust crate: no Rust engine runs it.
type ratingWithoutACrate struct{ repo string }

func (ratingWithoutACrate) Name() string { return "acme" }

func (r ratingWithoutACrate) Register(reg *registry.Registry) error {
	declaration, err := os.ReadFile(filepath.Join(r.repo, "examples", "acme-schematic", "ext", "rating.behavior.json"))
	if err != nil {
		return err
	}
	// Its rate operation names a value of acme's confirm policy, as the
	// acme-schematic example registers it.
	return errors.Join(
		reg.RegisterBehavior(registry.BehaviorSpec{Extension: "acme", Declaration: json.RawMessage(declaration)}),
		reg.RegisterToolInvocationPolicy(registry.ToolInvocationPolicy{Extension: "acme", Key: "confirm", Values: []string{"never", "always"}, Default: "never"}),
	)
}

// TestAnEngineNoRustEngineRunsIsRefused: when queue-db's Job composes an
// extension's behavior whose registration names no Rust crate, no project
// engine crate is written, and the stack's build refuses queue-api's Go
// server, saying which behavior (D58).
func TestAnEngineNoRustEngineRunsIsRefused(t *testing.T) {
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{}, ratingWithoutACrate{repo: testpaths.RepoRoot(t)})
	if err != nil {
		t.Fatal(err)
	}
	f := loadEngineFixture(t)
	f.reg = reg
	db := *f.schemas["queue-db"]
	job := *db.Types["Job"]
	job.Behaviors = append(slices.Clone(job.Behaviors), ir.BehaviorRef{Name: "acme.Rating", Config: json.RawMessage(`{"maxStars":5}`)})
	db.Types = map[string]*ir.TypeDef{"Job": &job}
	f.schemas["queue-db"] = &db
	repoRoot := t.TempDir()
	f.build(t, repoRoot, fakePaths(repoRoot), "queue-db", "queue-api")
	if _, err := os.Stat(generator.EngineDir(filepath.Join(repoRoot, "schemas", "dist"), generator.LangRust, "queue-db")); !os.IsNotExist(err) {
		t.Errorf("queue-db's build wrote an engine crate: %v", err)
	}
	_, err = generator.Run(f.schemas[queueStack], f.configs[queueStack], f.options(repoRoot, fakePaths(repoRoot)))
	if err == nil || !strings.Contains(err.Error(), "stack queue-stack: server queue-api serves queue-api, whose engine database is queue-db, and no engine can run queue-db's behaviors, so no engine crate is written and an engine refuses the schema: acme.Rating (extension acme) names no Rust crate") {
		t.Fatalf("the build of queue-stack: %v", err)
	}
}

// opsCallersVariables are api's callers field as an environment sets it, ops
// an issuer of its own with an Ed25519 key: a caller that serves ops-api.
func opsCallersVariables(t *testing.T, api string, public ed25519.PublicKey, kid string) []string {
	t.Helper()
	jwk, err := json.Marshal(map[string]string{"kty": "OKP", "crv": "Ed25519", "kid": kid, "x": base64.RawURLEncoding.EncodeToString(public)})
	if err != nil {
		t.Fatal(err)
	}
	vars, err := ir.DerivedVariables(ir.CallersField(api), ir.ServiceAuth{Issuers: []*ir.ServiceAuthIssuer{{
		Issuer: "ops", Audience: api, Algorithms: []string{ir.AlgorithmEdDSA},
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

// buildProjectArchive builds the archive of the project's engine crate at
// crate with the checkout's scripts/engine-archive.sh, staged in a
// directory of the test's own, which it returns: the -L a Go build puts
// before the core's. CARGO_TARGET_DIR, when set, is the build's target
// directory, so runs share it.
func buildProjectArchive(t *testing.T, crate string) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command(filepath.Join(testpaths.RepoRoot(t), "scripts", "engine-archive.sh"), "--crate", filepath.Join(crate, projectengine.FFIDir), "--out", dir)
	cmd.Dir = testpaths.RepoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/engine-archive.sh --crate %s: %v\n%s", crate, err, out)
	}
	return dir
}

// goBuildWith builds the module at dir into binary with ldflags first in
// CGO_LDFLAGS.
func goBuildWith(t *testing.T, dir, binary, ldflags string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = dir
	// PWD names dir as the test's temporary directory spells it, which the
	// generated replace directives are relative to.
	cmd.Env = append(os.Environ(), "PWD="+dir, "CGO_LDFLAGS="+ldflags+" "+os.Getenv("CGO_LDFLAGS"), "GOWORK=off", "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build in %s: %v\n%s", dir, err, out)
	}
}

// TestEngineProjectArchiveServes: queue-db's engine crate, written by its
// build, builds its archive with the checkout's script, and queue-api's
// server and queue-db's runner link it in the core's place (D58). The
// server starts with no database up, names the engine in /readyz and
// answers its routes 503. With the Postgres engineDatabaseEnv names, it
// deploys queue-db's document, whose Job composes the work queue's Lease
// and Queue, which only the project's archive runs, and a verified service
// standing in for an end user creates a job and reads it back under the
// allowAll policy; the runner starts beside it.
func TestEngineProjectArchiveServes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the archive's build in -short mode")
	}
	if _, err := exec.LookPath("cargo"); err != nil {
		skipEngineServers(t, "cargo not available; skipping the engine archive's build")
	}
	paths := testpaths.Local(t)
	repoRoot := t.TempDir()
	f := loadEngineFixture(t)
	f.build(t, repoRoot, paths, queueOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	archive := buildProjectArchive(t, generator.EngineDir(out, generator.LangRust, "queue-db"))
	ldflags := "-L" + archive

	dir := servergen.ServerDir(out, queueStack, queueServer)
	goCommand(t, dir, "mod", "tidy")
	binary := filepath.Join(t.TempDir(), queueServer)
	goBuildWith(t, dir, binary, ldflags)
	runnerDir := servergen.ServerDir(out, queueStack, queueRunner)
	goCommand(t, runnerDir, "mod", "tidy")
	runnerBinary := filepath.Join(t.TempDir(), queueRunner)
	goBuildWith(t, runnerDir, runnerBinary, ldflags)

	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	env := opsCallersVariables(t, queueServer, public, "ops-key")
	offline := start(t, binary, append([]string{"QUEUE_DB_DATABASE_URL=postgres://queue@127.0.0.1:9/queue_db?connect_timeout=1&sslmode=disable"}, env...)...)
	offline.expect(t, http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"queue-db engine"`)
	offline.expect(t, http.MethodGet, "/engine/queue-db/namespaces/queue-db/schemas", http.StatusServiceUnavailable, `"code":"unavailable"`)
	offline.stop(t)

	postgres := engineDatabase(t)
	if postgres == "" {
		t.Logf("%s is unset: the entrypoint serves no database", engineDatabaseEnv)
		return
	}
	tool := engineDatabaseTool(t, dir)
	databaseURL := tool.create(t, postgres, filepath.Join(generator.SQLDir(out, "queue-db"), "create.sql"))
	online := start(t, binary, append([]string{"QUEUE_DB_DATABASE_URL=" + databaseURL}, env...)...)
	online.waitReady(t)
	if !strings.Contains(online.out.String(), `"archive":"schemas-queue-db-engine-ffi"`) {
		t.Errorf("the server did not open the project's archive:\n%s", online.out.String())
	}
	const jobs = "/engine/queue-db/namespaces/queue-db/schemas/Job/instances"
	credential := signEdgeToken(t, key, "ops-key", "ops", queueServer)
	online.serviceSend(t, credential, http.MethodPost, jobs, `{"id": "j1", "data": {"title": "Reindex", "priority": 3}}`, http.StatusCreated, `"id":"j1"`, `"status":"queued"`)
	online.serviceSend(t, credential, http.MethodGet, jobs+"/j1", "", http.StatusOK, `"title":"Reindex"`, `"Lease"`)
	runner := startBackground(t, runnerBinary, "QUEUE_DB_DATABASE_URL="+databaseURL)
	runner.stop(t)
	online.stop(t)
}

// serviceSend sends method to path with a service credential, no end
// user's and body, a JSON one unless empty, checks the status and that the
// answer holds each of contains, and returns the answer.
func (s *started) serviceSend(t *testing.T, credential, method, path, body string, status int, contains ...string) string {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, s.base+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Service-Authorization", "Bearer "+credential)
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
		t.Errorf("%s %s with a service credential = %d, want %d: %s", method, path, resp.StatusCode, status, answer)
	}
	for _, want := range contains {
		if !strings.Contains(string(answer), want) {
			t.Errorf("%s %s answered %s, which lacks %q", method, path, answer, want)
		}
	}
	return string(answer)
}

// TestTheEngineProjectDockerfileBuildsAnImageThatServes: queue-api's
// Dockerfile builds from a repository root holding the generated modules,
// the project's engine crate of queue-db that its build wrote, the runtime
// modules, the engine's Go binding and the Cargo workspace beside it with
// the crates it reaches, and a superscalar checkout: its engine stage
// builds the archive of the project crate's ffi member, its lock file
// seeded from the engine workspace's, which the server links (D58). The
// image serves /healthz, names the engine in /readyz with no database up,
// answers the engine's routes 503, and stops cleanly on docker stop. It
// needs Docker and builds the engine in a Rust stage, so it runs only
// outside -short.
func TestTheEngineProjectDockerfileBuildsAnImageThatServes(t *testing.T) {
	needDocker(t)
	repoRoot := dockerRepo(t)
	repo := testpaths.RepoRoot(t)
	copyTree(t, filepath.Join(repo, "runtime", "engine", "go"), filepath.Join(repoRoot, "runtime", "engine", "go"), "lib")
	for _, dir := range []string{"runtime/engine/rust", "runtime/engine-workqueue/rust", "runtime/engine-commerce/rust", "runtime/schema/rust-ir", "runtime/schema/rust", "runtime/versiongraph/rust-engine", "runtime/versiongraph/rust"} {
		copyTree(t, filepath.Join(repo, filepath.FromSlash(dir)), filepath.Join(repoRoot, filepath.FromSlash(dir)))
	}
	f := loadEngineFixture(t)
	f.build(t, repoRoot, fakePaths(repoRoot), queueOrder...)

	image := fmt.Sprintf("superschematic-servergen-test:%d-engine-project", time.Now().UnixNano())
	dockerBuild(t, repoRoot, image, "-f", "schemas/dist/server/"+queueStack+"/"+queueServer+"/Dockerfile")
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "-d", "-p", "127.0.0.1::8080",
		"-e", "QUEUE_DB_DATABASE_URL=postgres://queue@127.0.0.1:9/queue_db?connect_timeout=1&sslmode=disable"}
	for _, v := range opsCallersVariables(t, queueServer, public, "ops-key") {
		args = append(args, "-e", v)
	}
	container := docker(t, append(args, image)...)
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
	s.expect(t, http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"queue-db engine"`)
	s.expect(t, http.MethodGet, "/engine/queue-db/namespaces/queue-db/schemas", http.StatusServiceUnavailable, `"code":"unavailable"`)
	docker(t, "stop", container)
	if code := docker(t, "inspect", "-f", "{{.State.ExitCode}}", container); code != "0" {
		t.Errorf("the container exited %s on docker stop:\n%s", code, docker(t, "logs", container))
	}
}

// TestAReleaseImageRefusesAProjectsEngine: a project with no [paths] keys,
// built by a release, gets queue-db's engine crate on the engine's crates
// by version, which no release ships (D58, amended): queue-api's server
// gets no Dockerfile, and the build says what to set; its link file names
// the crate with no lock file, and the local target refuses the crate.
func TestAReleaseImageRefusesAProjectsEngine(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadEngineFixture(t)
	var log strings.Builder
	for _, name := range queueOrder {
		opts := f.options(repoRoot, naming.LocalPaths{})
		opts.Release, opts.Log = &testRelease, &log
		if _, err := generator.Run(f.schemas[name], f.configs[name], opts); err != nil {
			t.Fatalf("build %s: %v", name, err)
		}
	}
	out := filepath.Join(repoRoot, "schemas", "dist")
	manifest, err := os.ReadFile(filepath.Join(generator.EngineDir(out, generator.LangRust, "queue-db"), projectengine.ManifestFile))
	if err != nil || !strings.Contains(string(manifest), "\nsuperschematic-engine = { version = \"0.0.0\", default-features = false }") {
		t.Fatalf("queue-db's crate does not depend on the engine by version (%v):\n%s", err, manifest)
	}
	dir := servergen.ServerDir(out, queueStack, queueServer)
	if _, err := os.Stat(filepath.Join(dir, servergen.DockerFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the build wrote a Dockerfile: %v", err)
	}
	if want := "server queue-api: no Dockerfile, since the engine of queue-db composes behaviors the release's engine archive does not run, so the image builds the archive of the project's engine crate"; !strings.Contains(log.String(), want) || !strings.Contains(log.String(), "no release ships (D58, amended)") {
		t.Errorf("the log does not say why:\n%s", log.String())
	}
	link, err := projectengine.ReadLink(dir)
	if err != nil || link == nil || link.Lockfile != "" || link.Member != projectengine.FFIDir {
		t.Errorf("queue-api's link file is %+v (%v), want the ffi member with no lock file", link, err)
	}
}

// TestEngineLockfile: the engine workspace's lock file comes from whichever
// [paths] key locates the workspace, engine_rust alone included, and from
// none without one.
func TestEngineLockfile(t *testing.T) {
	for _, tc := range []struct {
		paths naming.LocalPaths
		want  string
	}{
		{naming.LocalPaths{EngineRust: "/ss/runtime/engine/rust/engine"}, "/ss/runtime/engine/rust/Cargo.lock"},
		{naming.LocalPaths{EngineGo: "/ss/runtime/engine/go"}, "/ss/runtime/engine/rust/Cargo.lock"},
		{naming.LocalPaths{EngineTypeScript: "/ss/runtime/engine/typescript"}, "/ss/runtime/engine/rust/Cargo.lock"},
		{naming.LocalPaths{ScalarGo: "/ss/third_party/superscalar/go"}, ""},
	} {
		if got := servergen.EngineLockfile(tc.paths); got != filepath.FromSlash(tc.want) {
			t.Errorf("EngineLockfile(%+v) = %q, want %q", tc.paths, got, tc.want)
		}
	}
}
