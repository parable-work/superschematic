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

// rustGolden is the workflow of a stack whose Rust servers stand beside a
// Go one, which TestRustServersInCheck renders.
const rustGolden = "testdata/golden/stock-stack.yml"

// stockRequest is storefrontRequest's stack with a Go server and two Rust
// ones in place of the TypeScript servers (D57).
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
	}
	req.Environments[0].Environment = &env
	return req
}

// TestRustServersInCheck: check sets up the Rust release tools.env pins
// before the build, which brings the output root's Cargo lockfile up to
// date with it, then checks the lockfile is committed and the build left
// it as it was, and compiles each Rust server frozen to it (D57). No other
// job sets up Rust. The golden, which -update rewrites, shows the job.
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
		"Compile server pricing": "schemas/dist/server/stock-stack/pricing",
		"Compile server stock":   "schemas/dist/server/stock-stack/stock",
	} {
		if s := check.step(name); s.WorkingDirectory != dir || s.Run != "cargo check --locked" {
			t.Errorf("%s: %+v", name, s)
		}
	}
	for _, id := range w.order[1:] {
		if w.jobs[id].step("Set up Rust") != nil {
			t.Errorf("%s sets up Rust, which only check does", id)
		}
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
