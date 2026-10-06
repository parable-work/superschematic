package servergen_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

var update = flag.Bool("update", false, "rewrite the golden entrypoints")

const (
	// servicesRoot holds the fixture: shop-stack, whose declared server
	// Storefront serves shop-orders and shop-reviews and calls shop-api,
	// which runs on a default server of its own, and shop-db, the one
	// database of the three APIs.
	servicesRoot = "testdata/services"

	// goldenRoot holds the entrypoints as the output root lays them out,
	// under server, and the scaffolds as the repository root does, under
	// go.
	goldenRoot = "testdata/golden"
)

// order is the order a build-all builds the fixture in: shop-orders calls
// shop-api, so its server builds after shop-api's SDK.
var order = []string{"shop-db", "shop-api", "shop-orders", "shop-reviews", "shop-stack"}

// fixture is the loaded fixture services.
type fixture struct {
	schemas map[string]*ir.Schema
	configs map[string]*schemaconfig.SchemaConfig
}

func loadFixture(t *testing.T, root string) fixture {
	t.Helper()
	f := fixture{schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range order {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	return f
}

// options are one service's build in the repository at repoRoot, whose
// schemas root is repoRoot/schemas, as the CLI sets them.
func (f fixture) options(repoRoot string, paths naming.LocalPaths) generator.Options {
	return generator.Options{
		OutputRoot:     filepath.Join(repoRoot, "schemas", "dist"),
		Paths:          paths,
		Naming:         naming.Default(),
		Clock:          codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		RepositoryRoot: repoRoot,
		LoadDependency: func(name string) (*ir.Schema, error) {
			if schema, ok := f.schemas[name]; ok {
				return schema, nil
			}
			return nil, fmt.Errorf("no fixture %s", name)
		},
		DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
			cfg, ok := f.configs[name]
			return cfg, ok
		},
		LoadDependencyConfig: func(name string) (*schemaconfig.SchemaConfig, error) {
			if cfg, ok := f.configs[name]; ok {
				return cfg, nil
			}
			return nil, fmt.Errorf("no fixture %s", name)
		},
	}
}

// build builds services, or every fixture service in order, into the
// repository at repoRoot.
func (f fixture) build(t *testing.T, repoRoot string, paths naming.LocalPaths, services ...string) *generator.Result {
	t.Helper()
	if len(services) == 0 {
		services = order
	}
	var result *generator.Result
	for _, name := range services {
		var err error
		result, err = generator.Run(f.schemas[name], f.configs[name], f.options(repoRoot, paths))
		if err != nil {
			t.Fatalf("build %s: %v", name, err)
		}
	}
	return result
}

// fakePaths places the runtime modules in the repository at repoRoot, as
// this repository's naming file does.
func fakePaths(repoRoot string) naming.LocalPaths {
	return naming.LocalPaths{
		ScalarGo:        filepath.Join(repoRoot, "third_party", "superscalar", "go"),
		SchemaIR:        filepath.Join(repoRoot, "ir"),
		SchemaRuntimeGo: filepath.Join(repoRoot, "runtime", "schema", "go"),
		HTTPRuntimeGo:   filepath.Join(repoRoot, "runtime", "http", "go"),
		VersionGraphGo:  filepath.Join(repoRoot, "runtime", "versiongraph", "go"),
	}
}

// generated lists the files the stack's build writes, by their path under
// goldenRoot: an entrypoint's from the output root, a scaffold's from the
// repository root.
func generated(repoRoot string) map[string]string {
	files := map[string]string{}
	out := filepath.Join(repoRoot, "schemas", "dist")
	for _, server := range []string{"Storefront", "shop-api"} {
		for _, file := range []string{servergen.MainFile, servergen.ModFile, servergen.DockerFile, servergen.DockerIgnoreFile} {
			files[filepath.Join("server", "shop-stack", server, file)] = filepath.Join(servergen.ServerDir(out, "shop-stack", server), file)
		}
	}
	for _, service := range []string{"shop-api", "shop-orders", "shop-reviews"} {
		dir := naming.Default().GoImplementationDir(repoRoot, service)
		for _, file := range []string{apigen.ImplementationFile, servergen.ModFile} {
			files[filepath.Join("go", service, file)] = filepath.Join(dir, file)
		}
	}
	return files
}

// TestEntrypointGolden: the stack's build writes an entrypoint module per
// server, Storefront serving two APIs on one database and calling
// shop-api, and shop-api's default server, and scaffolds each API's
// implementation with a module of its own. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestEntrypointGolden -update
func TestEntrypointGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	result := f.build(t, repoRoot, fakePaths(repoRoot))
	if got, want := result.Outputs["server"], servergen.StackDir(filepath.Join(repoRoot, "schemas", "dist"), "shop-stack"); got != want {
		t.Errorf("server output = %q, want %q", got, want)
	}

	files := generated(repoRoot)
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		got, err := os.ReadFile(files[rel])
		if err != nil {
			t.Fatal(err)
		}
		golden := filepath.Join(goldenRoot, rel)
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
			t.Fatalf("%v (run with -update to write it)", err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from %s; run with -update and review the diff", rel, golden)
		}
	}
	entries, err := os.ReadDir(servergen.StackDir(filepath.Join(repoRoot, "schemas", "dist"), "shop-stack"))
	if err != nil {
		t.Fatal(err)
	}
	var servers []string
	for _, e := range entries {
		servers = append(servers, e.Name())
	}
	if got := strings.Join(servers, " "); got != "Storefront shop-api" {
		t.Errorf("servers = %s, want Storefront and shop-api", got)
	}
}

// TestTheScaffoldNeverOverwrites: the stack's build writes an
// implementation only while its package is missing, never rewrites the
// one it wrote or the module beside it, and never writes into a package
// that holds a Go file.
func TestTheScaffoldNeverOverwrites(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	f.build(t, repoRoot, fakePaths(repoRoot))

	dir := naming.Default().GoImplementationDir(repoRoot, "shop-orders")
	file := filepath.Join(dir, apigen.ImplementationFile)
	edited := []byte("package shoporders\n\n// The engineer's code.\n")
	if err := os.WriteFile(file, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	mod := filepath.Join(dir, servergen.ModFile)
	editedMod := []byte("module example.com/mine/orders\n\ngo 1.26.4\n")
	if err := os.WriteFile(mod, editedMod, 0o644); err != nil {
		t.Fatal(err)
	}
	f.build(t, repoRoot, fakePaths(repoRoot), "shop-stack")
	for path, want := range map[string][]byte{file: edited, mod: editedMod} {
		if got, err := os.ReadFile(path); err != nil || string(got) != string(want) {
			t.Errorf("a second build rewrote %s: %q, %v", path, got, err)
		}
	}
	main, err := os.ReadFile(filepath.Join(servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), "shop-stack", "Storefront"), servergen.MainFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(main), `shopordersimpl "example.com/mine/orders"`) {
		t.Errorf("the entrypoint does not import the implementation from its renamed module:\n%s", main)
	}

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "orders.go"), edited, 0o644); err != nil {
		t.Fatal(err)
	}
	f.build(t, repoRoot, fakePaths(repoRoot), "shop-stack")
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a build scaffolded into a package that holds orders.go: %v", err)
	}
}

// TestAnImplementationInNoModuleFails: a package the build did not
// scaffold, which no go.mod holds, cannot be imported, and the build says
// where to add one.
func TestAnImplementationInNoModuleFails(t *testing.T) {
	repoRoot := t.TempDir()
	dir := naming.Default().GoImplementationDir(repoRoot, "shop-reviews")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reviews.go"), []byte("package shopreviews\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := loadFixture(t, servicesRoot)
	f.build(t, repoRoot, fakePaths(repoRoot), order[:len(order)-1]...)
	_, err := generator.Run(f.schemas["shop-stack"], f.configs["shop-stack"], f.options(repoRoot, fakePaths(repoRoot)))
	if err == nil || !strings.Contains(err.Error(), "server Storefront serves shop-reviews, whose implementation "+dir+" is in no Go module; add a go.mod at it or above it") {
		t.Fatalf("build = %v, want the missing module named", err)
	}
	if _, err := os.Stat(naming.Default().GoImplementationDir(repoRoot, "shop-orders")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused build scaffolded shop-orders: %v", err)
	}
}

// TestTwoAPIsOnOneRouteFail: two APIs one server serves cannot register
// one method and path, whatever their path parameters are named, and the
// build names both.
func TestTwoAPIsOnOneRouteFail(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	reviews := f.schemas["shop-reviews"].OperationSets[0].Operations[0]
	reviews.RestPath = "orders/{productId}"
	_, err := generator.Run(f.schemas["shop-stack"], f.configs["shop-stack"], f.options(repoRoot, fakePaths(repoRoot)))
	want := "stack shop-stack: server Storefront serves APIs that register one route twice, and one router answers each method and path once: shop-orders registers GET /api/orders/{id} and shop-reviews registers GET /api/orders/{productId}"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("build = %v, want %q", err, want)
	}
	if entries, _ := os.ReadDir(repoRoot); len(entries) != 0 {
		t.Errorf("a refused build wrote %v", entries)
	}
}

// TestNoRepositoryRootWritesNoEntrypoint: a build that has no repository
// root, such as a library caller's, cannot find the implementations, so it
// writes no entrypoint and says so.
func TestNoRepositoryRootWritesNoEntrypoint(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	opts := f.options(repoRoot, fakePaths(repoRoot))
	opts.RepositoryRoot = ""
	result, err := generator.Run(f.schemas["shop-stack"], f.configs["shop-stack"], opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Outputs["server"]; ok || !strings.Contains(strings.Join(result.Skipped, " "), "server") {
		t.Errorf("outputs %v, skipped %v; want server skipped", result.Outputs, result.Skipped)
	}
}

// goCommand runs go in dir.
func goCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// freePort returns a port nothing listens on.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

// started is a server binary running on a port of its own.
type started struct {
	cmd  *exec.Cmd
	base string
	out  *strings.Builder
}

// start runs binary with env and waits for /healthz.
func start(t *testing.T, binary string, env ...string) *started {
	t.Helper()
	port := freePort(t)
	var out strings.Builder
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), append(env, "PORT="+port)...)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &started{cmd: cmd, base: "http://127.0.0.1:" + port, out: &out}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := http.Get(s.base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not answer /healthz: %v\n%s", binary, err, out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// expect sends method to path and checks the status, and that the body
// holds each of contains.
func (s *started) expect(t *testing.T, method, path string, status int, contains ...string) {
	t.Helper()
	req, err := http.NewRequest(method, s.base+path, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != status {
		t.Errorf("%s %s = %d, want %d: %s", method, path, resp.StatusCode, status, body)
	}
	for _, want := range contains {
		if !strings.Contains(string(body), want) {
			t.Errorf("%s %s answered %s, which lacks %q", method, path, body, want)
		}
	}
}

// stop sends SIGTERM and checks the server drains and exits cleanly.
func (s *started) stop(t *testing.T) {
	t.Helper()
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the server exited with %v after SIGTERM:\n%s", err, s.out.String())
		}
		if !strings.Contains(s.out.String(), `"msg":"stopped"`) {
			t.Errorf("the server did not log its graceful stop:\n%s", s.out.String())
		}
	case <-time.After(15 * time.Second):
		t.Errorf("the server did not stop within 15s of SIGTERM:\n%s", s.out.String())
	}
}

// TestEntrypointCompilesAndServes: each server's entrypoint builds with
// its scaffolded implementations and vets. Storefront starts with no
// database up, answers /healthz and reports the database in /readyz, and
// routes each API's requests to it, the scaffold answering 501; shop-api's
// scaffolded auth middleware refuses its protected route. Each stops
// cleanly on SIGTERM.
func TestEntrypointCompilesAndServes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	f.build(t, repoRoot, paths)

	out := filepath.Join(repoRoot, "schemas", "dist")
	binaries := map[string]string{}
	for _, server := range []string{"Storefront", "shop-api"} {
		dir := servergen.ServerDir(out, "shop-stack", server)
		goCommand(t, dir, "mod", "tidy")
		goCommand(t, dir, "vet", ".")
		binaries[server] = filepath.Join(t.TempDir(), server)
		goCommand(t, dir, "build", "-o", binaries[server], ".")
	}
	for _, service := range []string{"shop-api", "shop-orders", "shop-reviews"} {
		dir := naming.Default().GoImplementationDir(repoRoot, service)
		goCommand(t, dir, "mod", "tidy")
		goCommand(t, dir, "build", "./...")
	}

	// Port 9 discards; nothing answers a database there. The client of
	// shop-api signs its service credential with an edge key, as the local
	// target's connector derives it, which the server reads at startup.
	unreachable := "postgres://shop@127.0.0.1:9/shop_db?connect_timeout=1&sslmode=disable"
	storefront := start(t, binaries["Storefront"], append([]string{"SHOP_DB_DATABASE_URL=" + unreachable}, edgeVariables(t, "SHOP_API_SERVICE")...)...)
	storefront.expect(t, http.MethodGet, "/healthz", http.StatusOK, `"ok"`)
	storefront.expect(t, http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"unavailable":["shop-db"]`)
	storefront.expect(t, http.MethodGet, "/api/orders/o-1", http.StatusNotImplemented, "Order.GetOrder")
	storefront.expect(t, http.MethodGet, "/api/products/p-1/reviews", http.StatusNotImplemented, "Review.ListReviews")
	storefront.expect(t, http.MethodGet, "/api/openapi.json", http.StatusOK, "/api/orders/{id}")
	storefront.expect(t, http.MethodGet, "/api/nowhere", http.StatusNotFound)
	storefront.stop(t)

	shopAPI := start(t, binaries["shop-api"], "SHOP_DB_DATABASE_URL="+unreachable)
	shopAPI.expect(t, http.MethodGet, "/api/products/p-1", http.StatusNotImplemented, "Product.GetProduct")
	shopAPI.expect(t, http.MethodPut, "/api/products/p-1/name?name=x", http.StatusUnauthorized)
	shopAPI.stop(t)
}

// edgeVariables are the variables of the service field named field, an
// endpoint whose credential is a token signed with a fresh Ed25519 key,
// encoded as ir.DerivedVariables encodes a resolved environment's value.
func edgeVariables(t *testing.T, field string) []string {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := json.Marshal(map[string]string{
		"kty": "OKP", "crv": "Ed25519", "kid": "edge-1",
		"d": base64.RawURLEncoding.EncodeToString(private.Seed()),
		"x": base64.RawURLEncoding.EncodeToString(public),
	})
	if err != nil {
		t.Fatal(err)
	}
	vars, err := ir.DerivedVariables(field, ir.ServiceEndpoint{URL: "http://127.0.0.1:9", Credential: &ir.ServiceCredential{
		Source: ir.CredentialSignedToken, Audience: "shop-api", Issuer: "Storefront", Key: string(key),
	}})
	if err != nil {
		t.Fatal(err)
	}
	env := make([]string, len(vars))
	for i, v := range vars {
		env[i] = fmt.Sprintf("%s=%v", v.Name, v.Value)
	}
	return env
}

// withServiceClause gives shop-api's renameProduct an @allowService clause
// listing shop-orders, beside its user clause.
func withServiceClause(f fixture) {
	for _, set := range f.schemas["shop-api"].OperationSets {
		for _, op := range set.Operations {
			if op.Name == "renameProduct" {
				op.ServiceCallers = &ir.ServiceCallers{Mode: ir.ServiceCallersAllow, From: []string{"shop-orders"}}
			}
		}
	}
}

// TestAServiceClauseTakesTheServiceAuthenticator: an API whose operations
// have a service clause gets its Config's service authenticator from the
// entrypoint's serviceAuthenticator, and an API without one gets none.
func TestAServiceClauseTakesTheServiceAuthenticator(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	withServiceClause(f)
	f.build(t, repoRoot, fakePaths(repoRoot), "shop-stack")
	out := filepath.Join(repoRoot, "schemas", "dist")
	main, err := os.ReadFile(filepath.Join(servergen.ServerDir(out, "shop-stack", "shop-api"), servergen.MainFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`shopApiServiceAuthenticator, err := serviceAuthenticator("shop-api")`,
		"ServiceAuthenticator: shopApiServiceAuthenticator,",
		"func serviceAuthenticator(api string) (serviceauth.Authenticator, error) {",
	} {
		if !strings.Contains(string(main), want) {
			t.Errorf("shop-api's main.go lacks %q:\n%s", want, main)
		}
	}
	storefront, err := os.ReadFile(filepath.Join(servergen.ServerDir(out, "shop-stack", "Storefront"), servergen.MainFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(storefront), "serviceAuthenticator") {
		t.Errorf("Storefront, whose APIs have no service clause, has a service authenticator:\n%s", storefront)
	}
}

// TestAServiceClauseRefusesToStartWithoutTheServiceAuthField: until a
// connector derives the service-auth field, the server of an API with a
// service clause does not start, and says why.
func TestAServiceClauseRefusesToStartWithoutTheServiceAuthField(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	withServiceClause(f)
	f.build(t, repoRoot, paths)
	dir := servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), "shop-stack", "shop-api")
	goCommand(t, dir, "mod", "tidy")
	binary := filepath.Join(t.TempDir(), "shop-api")
	goCommand(t, dir, "build", "-o", binary, ".")
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), "PORT="+freePort(t), "SHOP_DB_DATABASE_URL=postgres://shop@127.0.0.1:9/shop_db")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("shop-api started:\n%s", out)
	}
	if want := "shop-api has operations with a service clause, and no environment derives the service-auth field"; !strings.Contains(string(out), want) {
		t.Errorf("shop-api stopped without saying %q:\n%s", want, out)
	}
}

// TestToolchainPinsMatchToolsEnv: the go directive the modules state and
// the images the Dockerfile builds in are tools.env's pins.
func TestToolchainPinsMatchToolsEnv(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(testpaths.RepoRoot(t), "tools.env"))
	if err != nil {
		t.Fatal(err)
	}
	pins := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(key, "#") {
			pins[key] = value
		}
	}
	if pins["GO_VERSION"] != servergen.GoVersion {
		t.Errorf("tools.env pins Go %s; servergen.GoVersion is %s", pins["GO_VERSION"], servergen.GoVersion)
	}
	if pins["RUST_VERSION"] != servergen.RustVersion {
		t.Errorf("tools.env pins Rust %s; servergen.RustVersion is %s", pins["RUST_VERSION"], servergen.RustVersion)
	}
}

// copyTree copies the directory src to dst, leaving out build products:
// target and node_modules directories, and skip, a directory under src.
func copyTree(t *testing.T, src, dst string, skip ...string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "target" || d.Name() == "node_modules" || slices.Contains(skip, filepath.ToSlash(rel)) {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// docker runs docker with args and returns its trimmed output.
func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestTheDockerfileBuildsAnImageThatServes: Storefront's Dockerfile
// builds from a repository root holding the generated modules, the
// scaffolds, the runtime modules and a superscalar checkout, and the image
// serves: /healthz, an API's route, and a clean stop on docker stop. It
// needs Docker, and builds superscalar's archive in a Rust stage, so it
// runs only outside -short.
func TestTheDockerfileBuildsAnImageThatServes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping docker build in -short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	if out, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		t.Skipf("docker is not running: %v\n%s", err, out)
	}
	local := testpaths.Local(t)
	repo := testpaths.RepoRoot(t)
	repoRoot := t.TempDir()
	for _, dir := range []string{"ir", "runtime/http/go", "runtime/schema/go", "third_party/superscalar/crates"} {
		copyTree(t, filepath.Join(repo, dir), filepath.Join(repoRoot, dir))
	}
	copyTree(t, local.ScalarGo, filepath.Join(repoRoot, "third_party", "superscalar", "go"), "lib")
	for _, file := range []string{"Cargo.toml", "Cargo.lock"} {
		data, err := os.ReadFile(filepath.Join(repo, "third_party", "superscalar", file))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repoRoot, "third_party", "superscalar", file), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := loadFixture(t, servicesRoot)
	f.build(t, repoRoot, fakePaths(repoRoot))

	image := fmt.Sprintf("superschematic-servergen-test:%d", time.Now().UnixNano())
	cmd := exec.Command("docker", "build", "-q", "-f", "schemas/dist/server/shop-stack/Storefront/Dockerfile", "-t", image, ".")
	cmd.Dir = repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", image).Run() })

	container := docker(t, "run", "-d", "-p", "127.0.0.1::8080",
		"-e", "SHOP_DB_DATABASE_URL=postgres://shop@127.0.0.1:9/shop_db?connect_timeout=1",
		"-e", "SHOP_API_SERVICE_URL=http://127.0.0.1:9", image)
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
	s.expect(t, http.MethodGet, "/api/orders/o-1", http.StatusNotImplemented, "Order.GetOrder")
	s.expect(t, http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"unavailable":["shop-db"]`)
	docker(t, "stop", container)
	if code := docker(t, "inspect", "-f", "{{.State.ExitCode}}", container); code != "0" {
		t.Errorf("the container exited %s on docker stop:\n%s", code, docker(t, "logs", container))
	}
	if logs := docker(t, "logs", container); !strings.Contains(logs, `"msg":"stopped"`) {
		t.Errorf("the container did not log its graceful stop:\n%s", logs)
	}
}
