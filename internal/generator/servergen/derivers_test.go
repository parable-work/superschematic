package servergen_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The deriver fixture (D74): triage-db, an engine database whose Ticket
// composes Derived; triage-ts-api and triage-rs-api, a TypeScript and a
// Rust API over it, each declaring TriageTickets, which fills Ticket, and
// FillAll, which names no schema, and each listing the memory store
// triage-cache (D75); and triage-stack, which deploys both on the local
// target.
const (
	triageStack = "triage-stack"
	triageTS    = "triage-ts-api-triage-tickets"
	triageTSAll = "triage-ts-api-fill-all"
	triageRS    = "triage-rs-api-triage-tickets"
)

// triageOrder is the order a build-all builds the deriver fixture in.
var triageOrder = []string{"triage-db", "triage-cache", "triage-ts-api", "triage-rs-api", triageStack}

// loadDeriverFixture loads the deriver fixture with the core registry and
// stacktest's fake target.
func loadDeriverFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range triageOrder {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(engineRoot, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	return f
}

// deriverPaths are the paths a build of the deriver fixture reads: the
// TypeScript and the Rust runtimes', and the engine's in both.
func deriverPaths(repoRoot string) naming.LocalPaths {
	paths := rustFakePaths(repoRoot)
	ts := tsEngineFakePaths(repoRoot)
	paths.ScalarTypeScript, paths.HTTPRuntimeTypeScript, paths.EngineTypeScript = ts.ScalarTypeScript, ts.HTTPRuntimeTypeScript, ts.EngineTypeScript
	return paths
}

// TestDeriverEntrypointsGolden: each API's Deps declares Derivers, a
// member per deriver with a method per derived field of its schemas, typed
// by the field through triage-db's types, and the scaffold builds them,
// each method failing until it is implemented; each deriver is an
// entrypoint of its own, in its API's language, which opens the engine for
// the API's Deps, acts as its deployable's name holding each schema's name
// and the Derived config's permission, and hands each stale field to its
// method (D74). Regenerate with:
//
//	go test ./internal/generator/servergen -run TestDeriverEntrypointsGolden -update
func TestDeriverEntrypointsGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadDeriverFixture(t)
	f.build(t, repoRoot, deriverPaths(repoRoot), triageOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	names := naming.Default()
	files := map[string]string{
		"api/triage-ts-api/deps.ts":                           filepath.Join(out, "api", "triage-ts-api", "deps.ts"),
		"api/triage-rs-api/src/deps.rs":                       filepath.Join(out, "api", "triage-rs-api", "src", "deps.rs"),
		"api/triage-rs-api/src/lib.rs":                        filepath.Join(out, "api", "triage-rs-api", "src", "lib.rs"),
		"api/triage-rs-api/Cargo.toml":                        filepath.Join(out, "api", "triage-rs-api", "Cargo.toml"),
		"typescript/triage-ts-api/index.ts":                   filepath.Join(names.TypeScriptImplementationDir(repoRoot, "triage-ts-api"), "index.ts"),
		"rust/triage-rs-api/" + rustrestgen.ImplementationLib: filepath.Join(names.RustImplementationDir(repoRoot, "triage-rs-api"), rustrestgen.ImplementationLib),
	}
	for _, file := range []string{servergen.TypeScriptMainFile, servergen.TypeScriptPackageFile} {
		files[filepath.Join("server", triageStack, triageTS, file)] = filepath.Join(servergen.ServerDir(out, triageStack, triageTS), file)
	}
	for _, file := range []string{servergen.RustMainFile, servergen.RustManifestFile} {
		files[filepath.Join("server", triageStack, triageRS, file)] = filepath.Join(servergen.ServerDir(out, triageStack, triageRS), filepath.FromSlash(file))
	}
	compareGoldens(t, filepath.Join(goldenRoot, "derivers"), files)
}

// TestDeriversOfTheStack: the stack resolves each deriver on the local
// target's deriver platform, a process of its own in its API's language,
// with FillAll filling every type of triage-db that composes Derived.
func TestDeriversOfTheStack(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadDeriverFixture(t)
	f.build(t, repoRoot, deriverPaths(repoRoot), triageOrder...)
	data, err := os.ReadFile(filepath.Join(repoRoot, "schemas", "dist", "stack", triageStack, "Local", "environment.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env ir.ResolvedEnvironment
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range env.Deployables {
		if d.Kind == ir.DeployableDeriver {
			got = append(got, d.Name+" "+d.Language+" "+d.Platform+" "+strings.Join(d.Deriver.Schemas, ","))
		}
	}
	slices.Sort(got)
	want := []string{
		"triage-rs-api-fill-all RUST local.deriver Ticket",
		"triage-rs-api-triage-tickets RUST local.deriver Ticket",
		"triage-ts-api-fill-all TYPESCRIPT local.deriver Ticket",
		"triage-ts-api-triage-tickets TYPESCRIPT local.deriver Ticket",
	}
	if !slices.Equal(got, want) {
		t.Errorf("derivers =\n%q\nwant\n%q", got, want)
	}
}

// TestDeriverRefusals: the build refuses a rules policy with no rule that
// names a deriver's subject and allows, and builds once one does; a
// deriver of a type that composes no Derived; and a Go API's deriver.
func TestDeriverRefusals(t *testing.T) {
	build := func(f fixture, services ...string) error {
		repoRoot := t.TempDir()
		var err error
		for _, name := range services {
			if _, err = generator.Run(f.schemas[name], f.configs[name], f.options(repoRoot, deriverPaths(repoRoot))); err != nil {
				return err
			}
		}
		return nil
	}
	withPolicy := func(f fixture, policy string) {
		cfg := *f.configs["triage-db"]
		outputs := map[string]any{}
		for k, v := range cfg.Outputs {
			outputs[k] = v
		}
		var parsed any
		if err := json.Unmarshal([]byte(policy), &parsed); err != nil {
			t.Fatal(err)
		}
		outputs["engine"] = map[string]any{"policy": parsed}
		cfg.Outputs = outputs
		f.configs["triage-db"] = &cfg
	}

	f := loadDeriverFixture(t)
	withPolicy(f, `{"rules": [{"require": "{schema}.{action}"}]}`)
	if err := build(f, "triage-db", "triage-ts-api"); err == nil || !strings.Contains(err.Error(), `deriver TriageTickets runs as triage-ts-api-triage-tickets, and the policy of triage-db's engine (outputs.engine.policy) has no rule that names it and allows; add {"subject": "triage-ts-api-triage-tickets", "allow": true} to its rules`) {
		t.Errorf("build = %v, want the rules policy refused", err)
	}
	f = loadDeriverFixture(t)
	withPolicy(f, `{"rules": [{"subject": ["triage-ts-api-fill-all", "triage-ts-api-triage-tickets"], "allow": true}, {"require": "{schema}.{action}"}]}`)
	if err := build(f, "triage-db", "triage-ts-api"); err != nil {
		t.Errorf("build = %v, want a policy that names each deriver to build", err)
	}

	f = loadDeriverFixture(t)
	f.schemas["triage-ts-api"].Derivers[0].Schemas = []string{"Note"}
	if err := build(f, "triage-db", "triage-ts-api"); err == nil || !strings.Contains(err.Error(), "deriver TriageTickets of triage-ts-api fills the derived fields of Note, which is no engine type of triage-db that composes Derived (its types that do: Ticket)") {
		t.Errorf("build = %v, want a schema without Derived refused", err)
	}

	f = loadDeriverFixture(t)
	goAPI := *f.configs["triage-ts-api"]
	goAPI.Outputs = map[string]any{"types": map[string]any{"go": map[string]any{"enabled": true}}, "api": map[string]any{"enabled": true}}
	f.configs["triage-ts-api"] = &goAPI
	dbConfig := *f.configs["triage-db"]
	dbConfig.Outputs = map[string]any{"types": map[string]any{"go": map[string]any{"enabled": true}, "typescript": map[string]any{"enabled": true}, "rust": map[string]any{"enabled": true}}}
	f.configs["triage-db"] = &dbConfig
	if err := build(f, "triage-db", "triage-ts-api"); err == nil || !strings.Contains(err.Error(), "triage-ts-api declares deriver TriageTickets, and it is a Go API; a deriver runs on a TypeScript or Rust API; Go follows (D74)") {
		t.Errorf("build = %v, want a Go API's deriver refused", err)
	}
}

// TestDeriverEntrypointsCompile: the Rust deriver's crate, its API's and
// the scaffolded implementation pass clippy in the output root's
// workspace, frozen to the lockfile the stack's build wrote, and its
// main.rs is laid out as rustfmt lays it out; the TypeScript deriver's
// package and the scaffolded implementation install in the output root's
// workspace with the engine's package and type-check (D74).
func TestDeriverEntrypointsCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	repoRoot, err := filepath.EvalSymlinks(testpaths.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	f := loadDeriverFixture(t)
	f.build(t, repoRoot, paths, triageOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")

	t.Run("rust", func(t *testing.T) {
		cargo, err := exec.LookPath("cargo")
		if err != nil {
			t.Skip("cargo not available; skipping the Rust build")
		}
		target := rustTargetDir(t)
		cargoCommand(t, cargo, out, target, "clippy", "--locked", "--workspace", "--quiet", "--", "-D", "warnings")
		if rustfmt, err := exec.LookPath("rustfmt"); err == nil {
			source := filepath.Join(servergen.ServerDir(out, triageStack, triageRS), filepath.FromSlash(servergen.RustMainFile))
			if diff, err := exec.Command(rustfmt, "--edition", "2021", "--check", source).CombinedOutput(); err != nil {
				t.Errorf("rustfmt lays out %s otherwise: %v\n%s", source, err, diff)
			}
		}
	})

	t.Run("typescript", func(t *testing.T) {
		bun, err := exec.LookPath("bun")
		if err != nil {
			testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
		}
		requireEngineTypeScript(t, paths.EngineTypeScript)
		installTypeScriptRuntime(t, bun, paths)
		install := exec.Command(bun, "install")
		install.Dir = out
		if output, err := install.CombinedOutput(); err != nil {
			testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
		}
		tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
		for _, pkg := range []string{servergen.ServerDir(out, triageStack, triageTS), servergen.ServerDir(out, triageStack, triageTSAll), naming.Default().TypeScriptImplementationDir(repoRoot, "triage-ts-api")} {
			cmd := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
			cmd.Dir = pkg
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("tsc in %s: %v\n%s", pkg, err, output)
			}
		}
	})
}
