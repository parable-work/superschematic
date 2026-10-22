package cigen_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/cigen"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
)

// rustGolden is the workflow of a stack whose Rust servers, job and worker
// stand beside a Go server, which TestRustServersInCheck renders.
const rustGolden = "testdata/golden/stock-stack.yml"

// stockRequest is storefrontRequest's stack with a Go server and two Rust
// ones in place of the TypeScript servers (D57), and a Rust job and a Rust
// worker of shop-stock (D65).
func stockRequest() publicregistry.CIRequest {
	req := storefrontRequest()
	req.Stack.Name, req.Stack.Dir = "stock-stack", "schemas/services/stock-stack"
	req.Stack.TypeScriptImplementations = nil
	env := *req.Environments[0].Environment
	env.Stack = "stock-stack"
	env.Deployables = []*ir.ResolvedDeployable{
		{Name: "Orders", Kind: ir.DeployableServer, Language: publicregistry.APILanguageGo, Services: []ir.ServiceRef{{Name: "shop-orders", Kind: ir.SchemaKindAPI}}},
		{Name: "pricing", Kind: ir.DeployableServer, Language: publicregistry.APILanguageRust, Services: []ir.ServiceRef{{Name: "shop-pricing", Kind: ir.SchemaKindAPI}}},
		{Name: "stock", Kind: ir.DeployableServer, Language: publicregistry.APILanguageRust, Services: []ir.ServiceRef{{Name: "shop-stock", Kind: ir.SchemaKindAPI}}},
		{Name: "shop-stock-reorder-stock", Kind: ir.DeployableWorker, Language: publicregistry.APILanguageRust, Services: []ir.ServiceRef{{Name: "shop-stock", Kind: ir.SchemaKindAPI}}},
		{Name: "shop-stock-place-restock-orders", Kind: ir.DeployableJob, Language: publicregistry.APILanguageRust, Services: []ir.ServiceRef{{Name: "shop-stock", Kind: ir.SchemaKindAPI}}},
		{Name: "stock-db-engine-runner", Kind: ir.DeployableRunner, Language: publicregistry.APILanguageRust, Services: []ir.ServiceRef{{Name: "stock-db", Kind: ir.SchemaKindDB}},
			Runner: &ir.ResolvedRunner{Database: "stock-db", API: "shop-stock", Instances: 1}},
	}
	req.Environments[0].Environment = &env
	return req
}

// TestRustServersInCheck: check sets up the Rust release tools.env pins
// before the build, which brings the output root's Cargo lockfile up to
// date with it, then checks the lockfile is committed and the build left
// it as it was, and compiles each Rust server frozen to it (D57), then
// each Rust job and worker (D65). Every
// other job that builds sets up the same release before its build, and
// a plan, which builds nothing, sets up none. The golden, which -update
// rewrites, shows the jobs.
func TestRustServersInCheck(t *testing.T) {
	files, err := cigen.GitHub().Render(stockRequest())
	if err != nil {
		t.Fatal(err)
	}
	got := files[0].Data
	if *update {
		if err := os.WriteFile(rustGolden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if want := readFile(t, rustGolden); !bytes.Equal(got, want) {
		t.Errorf("the workflow differs from %s; run with -update and review the diff", rustGolden)
	}
	lint(t, rustGolden)

	w := parseWorkflow(t, got)
	check := w.jobs["check"]
	var names []string
	for _, s := range check.Steps {
		names = append(names, s.Name)
	}
	lockfile := "Check the Rust workspace's lockfile schemas/dist/Cargo.lock"
	want := []string{
		"Check out", "Install superschematic " + version, "Install the static archives", "Set up Go", "Set up Rust",
		"Build the services", "Compile server Orders", lockfile, "Compile server pricing", "Compile server stock",
		"Compile job shop-stock-place-restock-orders", "Compile worker shop-stock-reorder-stock",
		"Compile runner stock-db-engine-runner",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("check's steps:\n%q\nwant:\n%q", names, want)
	}
	if s := check.step("Set up Rust"); !strings.Contains(s.Run, "rustup toolchain install "+servergen.RustVersion+" ") || !strings.Contains(s.Run, "rustup default "+servergen.RustVersion) {
		t.Errorf("check sets up Rust with %q, want tools.env's %s", s.Run, servergen.RustVersion)
	}
	if s := check.step(lockfile); s.WorkingDirectory != "schemas/dist" {
		t.Errorf("check checks the lockfile with %+v", s)
	}
	for name, dir := range map[string]string{
		"Compile server pricing":                      "schemas/dist/server/stock-stack/pricing",
		"Compile server stock":                        "schemas/dist/server/stock-stack/stock",
		"Compile job shop-stock-place-restock-orders": "schemas/dist/server/stock-stack/shop-stock-place-restock-orders",
		"Compile worker shop-stock-reorder-stock":     "schemas/dist/server/stock-stack/shop-stock-reorder-stock",
		"Compile runner stock-db-engine-runner":       "schemas/dist/server/stock-stack/stock-db-engine-runner",
	} {
		if s := check.step(name); s.WorkingDirectory != dir || s.Run != "cargo check --locked" {
			t.Errorf("%s: %+v", name, s)
		}
	}
	for _, id := range w.order[1:] {
		j := w.jobs[id]
		var names []string
		for _, s := range j.Steps {
			names = append(names, s.Name)
		}
		setup, build := slices.Index(names, "Set up Rust"), slices.Index(names, "Build the services")
		if build < 0 && setup >= 0 {
			t.Errorf("%s sets up Rust and builds nothing: %q", id, names)
		}
		if build >= 0 && setup != build-1 {
			t.Errorf("%s builds without setting up Rust first: %q", id, names)
		}
	}
	if w.jobs["prepare"].step("Set up Rust") == nil {
		t.Error("prepare builds the services without setting up Rust")
	}
	if w.jobs["deploy-staging"].step("Build the services") != nil {
		t.Error("deploy-staging builds the services, which prepare does (D47, amended)")
	}
}

// TestTheRustLockfileStepRefusesAMissingOrStaleLockfile runs check's step
// on the Cargo lockfile in a repository: a lockfile git does not track
// fails, saying it is not committed; one the build changed fails, saying
// it does not match the crates; a committed one the build left as it was
// passes.
func TestTheRustLockfileStepRefusesAMissingOrStaleLockfile(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	files, err := cigen.GitHub().Render(stockRequest())
	if err != nil {
		t.Fatal(err)
	}
	script := parseWorkflow(t, files[0].Data).jobs["check"].step("Check the Rust workspace's lockfile schemas/dist/Cargo.lock").Run
	repo := t.TempDir()
	out := filepath.Join(repo, "schemas", "dist")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run := func() (string, error) {
		cmd := exec.Command("bash", "-eo", "pipefail", "-c", script)
		cmd.Dir = out
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	gitIn("init", "--quiet")
	lock := filepath.Join(out, servergen.RustLockfileName)
	if err := os.WriteFile(lock, []byte("version = 4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	output, err := run()
	if want := "::error file=schemas/dist/Cargo.lock::schemas/dist/Cargo.lock is not committed; build every service (superschematic build-all), which brings schemas/dist/Cargo.lock up to date, and commit it"; err == nil || !strings.Contains(output, want) {
		t.Errorf("an untracked lockfile: %v\n%s", err, output)
	}
	gitIn("add", "schemas/dist/Cargo.lock")
	gitIn("commit", "--quiet", "-m", "lockfile")
	if output, err := run(); err != nil {
		t.Errorf("a committed lockfile the build left as it was: %v\n%s", err, output)
	}
	if err := os.WriteFile(lock, []byte("version = 4\n\n[[package]]\nname = \"itoa\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	output, err = run()
	if want := "::error file=schemas/dist/Cargo.lock::schemas/dist/Cargo.lock does not match the crates the build wrote"; err == nil || !strings.Contains(output, want) {
		t.Errorf("a lockfile the build changed: %v\n%s", err, output)
	}
}

// engineGolden is the workflow of a stack whose Go server opens an engine
// whose types compose an extension's behavior, which
// TestAProjectEngineArchiveInCheck renders.
const engineGolden = "testdata/golden/desk-stack.yml"

// TestAProjectEngineArchiveInCheck: a Go server on an engine database
// whose engine composes a behavior the core's archive does not run links
// the archive of the project's engine crate, which the build writes under
// the output root (D58). check sets up the Rust release tools.env pins,
// builds the archive from the crate's ffi member after the build, staging
// it as libsuperschematic_engine.a, and compiles the server and the
// database's engine runner with it searched before the release's; a server
// on another database compiles as before. The golden, which -update
// rewrites, shows the job.
func TestAProjectEngineArchiveInCheck(t *testing.T) {
	req := storefrontRequest()
	req.Stack.Name, req.Stack.Dir = "desk-stack", "schemas/services/desk-stack"
	req.Stack.TypeScriptImplementations = nil
	req.Stack.EngineArchives = map[string]string{"desk-db": "schemas/dist/engine/rust/desk-db/ffi"}
	env := *req.Environments[0].Environment
	env.Stack = "desk-stack"
	env.Deployables = []*ir.ResolvedDeployable{
		{Name: "desk-api", Kind: ir.DeployableServer, Language: publicregistry.APILanguageGo, Services: []ir.ServiceRef{{Name: "desk-api", Kind: ir.SchemaKindAPI}}},
		{Name: "Orders", Kind: ir.DeployableServer, Language: publicregistry.APILanguageGo, Services: []ir.ServiceRef{{Name: "shop-orders", Kind: ir.SchemaKindAPI}}},
		{Name: "desk-db-engine-runner", Kind: ir.DeployableRunner, Language: publicregistry.APILanguageGo, Services: []ir.ServiceRef{{Name: "desk-db", Kind: ir.SchemaKindDB}},
			Runner: &ir.ResolvedRunner{Database: "desk-db", API: "desk-api", Instances: 1}},
	}
	env.Edges = []*ir.Edge{
		{ID: "sql:desk-api->desk-db", Kind: ir.EdgeSQL, From: "desk-api", To: "desk-db", Service: ir.ServiceRef{Name: "desk-db", Kind: ir.SchemaKindDB}},
		{ID: "sql:desk-db-engine-runner->desk-db", Kind: ir.EdgeSQL, From: "desk-db-engine-runner", To: "desk-db", Service: ir.ServiceRef{Name: "desk-db", Kind: ir.SchemaKindDB}},
		{ID: "sql:Orders->shop-db", Kind: ir.EdgeSQL, From: "Orders", To: "shop-db", Service: ir.ServiceRef{Name: "shop-db", Kind: ir.SchemaKindDB}},
	}
	req.Environments[0].Environment = &env
	files, err := cigen.GitHub().Render(req)
	if err != nil {
		t.Fatal(err)
	}
	got := files[0].Data
	if *update {
		if err := os.WriteFile(engineGolden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if want := readFile(t, engineGolden); !bytes.Equal(got, want) {
		t.Errorf("the workflow differs from %s; run with -update and review the diff", engineGolden)
	}
	lint(t, engineGolden)

	check := parseWorkflow(t, got).jobs["check"]
	var names []string
	for _, s := range check.Steps {
		names = append(names, s.Name)
	}
	want := []string{
		"Check out", "Install superschematic " + version, "Install the static archives", "Set up Go", "Set up Rust",
		"Build the services", "Build the engine archive of desk-db", "Compile server Orders", "Compile server desk-api",
		"Compile runner desk-db-engine-runner",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("check's steps:\n%q\nwant:\n%q", names, want)
	}
	if s := check.step("Build the engine archive of desk-db"); s.WorkingDirectory != "schemas/dist/engine/rust/desk-db/ffi" ||
		!strings.Contains(s.Run, "cargo build --release") || !strings.Contains(s.Run, `"$dir/libsuperschematic_engine.a"`) {
		t.Errorf("the archive step: %+v", s)
	}
	for _, name := range []string{"Compile server desk-api", "Compile runner desk-db-engine-runner"} {
		if s := check.step(name); !strings.HasPrefix(s.Run, `CGO_LDFLAGS="-L$RUNNER_TEMP/superschematic-engine/desk-db $CGO_LDFLAGS" go build`) {
			t.Errorf("%s: %q, not against the project's archive", name, s.Run)
		}
	}
	if s := check.step("Compile server Orders"); s.Run != "go build -mod=mod -o /dev/null ." {
		t.Errorf("Orders, on another database, compiles with %q", s.Run)
	}
}

// TestAnEngineCheckoutInCheck: a stack whose naming file's [paths] engine
// keys name a superschematic checkout in the repository (D58). The
// checkout's engine crates reach superscalar's core in a checkout git does
// not hold, so check stands it up before the build; the project's engine
// crate starts from the engine workspace's lock file; and before the
// workspace's install it builds the checkout's TypeScript packages the
// workspace takes in place of the published ones, so the TypeScript server
// type-checks against the checkout's engine; it builds them only when
// [paths] engine_typescript names that checkout's package.
func TestAnEngineCheckoutInCheck(t *testing.T) {
	req := storefrontRequest()
	req.Stack.Name, req.Stack.Dir = "desk-stack", "schemas/services/desk-stack"
	req.Stack.TypeScriptImplementations = map[string]string{"desk-ts-api": "typescript/desk-ts-api"}
	req.Stack.EngineArchives = map[string]string{"desk-db": "schemas/dist/engine/rust/desk-db/ffi"}
	req.Stack.EngineCheckout = "vendor/superschematic"
	req.Stack.EngineCheckoutTypeScript = true
	env := *req.Environments[0].Environment
	env.Stack = "desk-stack"
	env.Deployables = []*ir.ResolvedDeployable{
		{Name: "desk-api", Kind: ir.DeployableServer, Language: publicregistry.APILanguageGo, Services: []ir.ServiceRef{{Name: "desk-api", Kind: ir.SchemaKindAPI}}},
		{Name: "desk-ts-api", Kind: ir.DeployableServer, Language: publicregistry.APILanguageTypeScript, Services: []ir.ServiceRef{{Name: "desk-ts-api", Kind: ir.SchemaKindAPI}}},
		{Name: "desk-db-engine-runner", Kind: ir.DeployableRunner, Language: publicregistry.APILanguageGo, Services: []ir.ServiceRef{{Name: "desk-db", Kind: ir.SchemaKindDB}},
			Runner: &ir.ResolvedRunner{Database: "desk-db", API: "desk-api", Instances: 1}},
	}
	env.Edges = []*ir.Edge{
		{ID: "sql:desk-api->desk-db", Kind: ir.EdgeSQL, From: "desk-api", To: "desk-db", Service: ir.ServiceRef{Name: "desk-db", Kind: ir.SchemaKindDB}},
		{ID: "sql:desk-ts-api->desk-db", Kind: ir.EdgeSQL, From: "desk-ts-api", To: "desk-db", Service: ir.ServiceRef{Name: "desk-db", Kind: ir.SchemaKindDB}},
		{ID: "sql:desk-db-engine-runner->desk-db", Kind: ir.EdgeSQL, From: "desk-db-engine-runner", To: "desk-db", Service: ir.ServiceRef{Name: "desk-db", Kind: ir.SchemaKindDB}},
	}
	req.Environments[0].Environment = &env
	files, err := cigen.GitHub().Render(req)
	if err != nil {
		t.Fatal(err)
	}
	got := files[0].Data
	path := filepath.Join(t.TempDir(), "desk-stack.yml")
	if err := os.WriteFile(path, got, 0o644); err != nil {
		t.Fatal(err)
	}
	lint(t, path)

	check := parseWorkflow(t, got).jobs["check"]
	var names []string
	for _, s := range check.Steps {
		names = append(names, s.Name)
	}
	want := []string{
		"Check out", "Install superschematic " + version, "Install the static archives", "Set up Go", "Set up Rust", "Set up Bun",
		"Check out superscalar for the engine's checkout vendor/superschematic",
		"Build the services", "Build the engine archive of desk-db", "Compile server desk-api", "Compile runner desk-db-engine-runner",
		"Build the TypeScript packages of the engine's checkout vendor/superschematic",
		"Install the TypeScript workspace from schemas/dist/bun.lock",
		"Type-check server desk-ts-api", "Type-check the implementation of desk-ts-api",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("check's steps:\n%q\nwant:\n%q", names, want)
	}
	if s := check.step("Check out superscalar for the engine's checkout vendor/superschematic"); s.WorkingDirectory != "vendor/superschematic" || s.Run != "scripts/superscalar-dep.sh --checkout" {
		t.Errorf("the superscalar step: %+v", s)
	}
	if s := check.step("Build the engine archive of desk-db"); !strings.Contains(s.Run, `test -f ../Cargo.lock || cp "$GITHUB_WORKSPACE/vendor/superschematic/runtime/engine/rust/Cargo.lock" ../Cargo.lock`) {
		t.Errorf("the archive step seeds no lock file: %+v", s)
	}
	s := check.step("Build the TypeScript packages of the engine's checkout vendor/superschematic")
	for _, want := range []string{
		"(cd third_party/superscalar/bindings/typescript && bun install --frozen-lockfile",
		"(cd runtime/http/typescript && bun install --frozen-lockfile && bun run build)",
		"(cd runtime/engine/typescript && bun install --frozen-lockfile && bun run build)",
	} {
		if s.WorkingDirectory != "vendor/superschematic" || !strings.Contains(s.Run, want) {
			t.Errorf("the TypeScript packages step lacks %q: %+v", want, s)
		}
	}

	// With the checkout found from engine_go alone, the workspace takes the
	// published TypeScript packages, and check builds none of the
	// checkout's.
	req.Stack.EngineCheckoutTypeScript = false
	files, err = cigen.GitHub().Render(req)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(files[0].Data, []byte("Check out superscalar for the engine's checkout")) || bytes.Contains(files[0].Data, []byte("Build the TypeScript packages of the engine's checkout")) {
		t.Errorf("a checkout of no engine_typescript builds its TypeScript packages, or no archive's superscalar:\n%s", files[0].Data)
	}

	// Without the checkout, check builds none of it.
	req.Stack.EngineCheckout = ""
	files, err = cigen.GitHub().Render(req)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(files[0].Data, []byte("engine's checkout")) || bytes.Contains(files[0].Data, []byte("../Cargo.lock")) {
		t.Errorf("a stack of no engine checkout builds one:\n%s", files[0].Data)
	}
}
