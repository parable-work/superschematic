package cigen_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/cigen"
	ir "github.com/parable-work/superschematic/ir"
)

// TestCheckBuildsTheBehaviorsCoreOfACheckout (D103): in a stack whose Go
// server Orders and TypeScript server storefront connect to databases
// whose tables' fields are behaviors, with the core's checkout at
// vendor/superschematic, check sets up Rust, stages the core's archive in
// the checkout after the build and before the Go compiles, and builds the
// checkout's @superschematic/rules before it installs the TypeScript
// workspace. Rendered as a release renders it, the Go server links the
// archive the release's tarball holds, so check stages none.
func TestCheckBuildsTheBehaviorsCoreOfACheckout(t *testing.T) {
	req := storefrontRequest()
	env := *req.Environments[0].Environment
	env.Edges = []*ir.Edge{
		{ID: "sql:Orders->shop-db", Kind: ir.EdgeSQL, From: "Orders", Service: ir.ServiceRef{Name: "shop-db", Kind: ir.SchemaKindDB}},
		{ID: "sql:storefront->shop-db", Kind: ir.EdgeSQL, From: "storefront", Service: ir.ServiceRef{Name: "shop-db", Kind: ir.SchemaKindDB}},
	}
	req.Environments[0].Environment = &env
	req.Stack.RulesCheckout = "vendor/superschematic"
	req.Stack.RulesGo = []string{"Orders"}
	req.Stack.RulesTypeScript = []string{"storefront"}

	stepsOf := func(t *testing.T) parsedJob {
		t.Helper()
		files, err := cigen.GitHub().Render(req)
		if err != nil {
			t.Fatal(err)
		}
		return parseWorkflow(t, files[0].Data).jobs["check"]
	}
	stage := "Stage the behaviors' core's archive in the checkout vendor/superschematic"
	build := "Build @superschematic/rules of the checkout vendor/superschematic"
	install := "Install the TypeScript workspace from schemas/dist/bun.lock"

	req.Version = ""
	check := stepsOf(t)
	var names []string
	for _, s := range check.Steps {
		names = append(names, s.Name)
	}
	index := func(name string) int { return slices.Index(names, name) }
	for _, name := range []string{"Set up Rust", stage, build, install, "Compile server Orders"} {
		if index(name) < 0 {
			t.Fatalf("check has no step %q:\n%q", name, names)
		}
	}
	if !(index("Set up Rust") < index("Build the services") && index("Build the services") < index(stage) && index(stage) < index("Compile server Orders")) {
		t.Errorf("check stages the archive out of order:\n%q", names)
	}
	if !(index(build) < index(install)) {
		t.Errorf("check builds @superschematic/rules after it installs the workspace:\n%q", names)
	}
	if s := check.step(stage); s.WorkingDirectory != "vendor/superschematic" || s.Run != "scripts/rules-archive.sh" {
		t.Errorf("%s: %+v", stage, s)
	}
	if s := check.step(build); s.WorkingDirectory != "vendor/superschematic" ||
		!strings.Contains(s.Run, "rustup target add wasm32-unknown-unknown") ||
		!strings.Contains(s.Run, "(cd runtime/rules/typescript && bun install --frozen-lockfile && bun run build)") {
		t.Errorf("%s: %+v", build, s)
	}

	req.Version = version
	check = stepsOf(t)
	if check.step(stage) != nil {
		t.Error("check of a release stages the core's archive, which the release's tarball holds")
	}
	if check.step("Install the static archives") == nil || check.step(build) == nil {
		t.Error("check of a release installs no static archives, or does not build @superschematic/rules, which no release ships")
	}
}
