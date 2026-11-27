package servergen_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
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

// The Go and Rust table deriver fixture (D146): fixture-derived-provenance-db,
// whose tables Ticket and Article implement Derived; derived-go-api and
// derived-rs-api, a Go and a Rust API over it, each declaring
// TriageTickets, which fills Ticket, and FillAll, which names no schema;
// and derived-tables-stack, which deploys both on the local target.
const (
	tablesDeriverStack = "derived-tables-stack"
	goDerivedAPI       = "derived-go-api"
	rsDerivedAPI       = "derived-rs-api"
	goTableDeriver     = "derived-go-api-triage-tickets"
	goTableDeriverAll  = "derived-go-api-fill-all"
	rsTableDeriver     = "derived-rs-api-triage-tickets"
	rsTableDeriverAll  = "derived-rs-api-fill-all"
	// tablesDeriverRoot holds the fixture's API and stack services, and
	// tablesDeriverGoldenRoot the deriver's files as the output root lays
	// them out.
	tablesDeriverRoot       = "testdata/behaviors/table-derivers/services"
	tablesDeriverGoldenRoot = "testdata/golden/table-derivers-go-rust"
)

// tablesDeriverOrder is the order a build-all builds the fixture in.
var tablesDeriverOrder = []string{tableDeriverDB, goDerivedAPI, rsDerivedAPI, tablesDeriverStack}

// loadTablesDeriverFixture loads the fixture with the core registry and
// stacktest's fake target. The database is the loader's fixture, whose
// config it narrows to what the derivers read: its Go and Rust types, and
// its Postgres DDL.
func loadTablesDeriverFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range tablesDeriverOrder {
		root := tablesDeriverRoot
		if name == tableDeriverDB {
			root = tsBehaviorsServicesRoot
		}
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(root, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	enabled := map[string]any{"enabled": true}
	f.configs[tableDeriverDB].Outputs = map[string]any{
		"types": map[string]any{"go": enabled, "rust": enabled},
		"sql":   map[string]any{"dialects": []any{"postgres"}},
	}
	return f
}

// tablesDeriverPaths are the paths a build of the fixture reads: the Go
// and the Rust runtimes', and the behaviors' core's in both.
func tablesDeriverPaths(repoRoot string) naming.LocalPaths {
	paths := rustFakePaths(repoRoot)
	paths.RulesGo = filepath.Join(repoRoot, "runtime", "rules", "go")
	paths.RulesAdapterRust = filepath.Join(repoRoot, "runtime", "engine", "rust", "rules-adapter")
	paths.RulesBehaviorsRust = filepath.Join(repoRoot, "runtime", "engine", "rust", "rules-behaviors")
	return paths
}

// TestGoRustTableDeriverEntrypointsGolden: a deriver of tables that
// implement Derived on a Go and on a Rust API (D146). Each API declares
// Derivers as a TypeScript API does, a member per deriver with a method per
// derived field of its tables, typed by the field through the database's
// types, and the scaffold builds them. Each deriver is an entrypoint of its
// own, which opens no engine: it opens the database's repositories as its
// deployable's name, holding each Derived config's permission, and runs
// its HTTP runtime's deriver loop over each table's derived operations,
// handing each stale field to its method. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestGoRustTableDeriverEntrypointsGolden -update
func TestGoRustTableDeriverEntrypointsGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTablesDeriverFixture(t)
	f.build(t, repoRoot, tablesDeriverPaths(repoRoot), tablesDeriverOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	names := naming.Default()
	files := map[string]string{
		"api/derived-go-api/deps.go":                           filepath.Join(out, "api", goDerivedAPI, "deps.go"),
		"go/derived-go-api/" + apigen.ImplementationFile:       filepath.Join(names.GoImplementationDir(repoRoot, goDerivedAPI), apigen.ImplementationFile),
		"api/derived-rs-api/src/deps.rs":                       filepath.Join(out, "api", rsDerivedAPI, "src", "deps.rs"),
		"api/derived-rs-api/src/lib.rs":                        filepath.Join(out, "api", rsDerivedAPI, "src", "lib.rs"),
		"api/derived-rs-api/Cargo.toml":                        filepath.Join(out, "api", rsDerivedAPI, "Cargo.toml"),
		"rust/derived-rs-api/" + rustrestgen.ImplementationLib: filepath.Join(names.RustImplementationDir(repoRoot, rsDerivedAPI), rustrestgen.ImplementationLib),
	}
	for _, file := range []string{servergen.MainFile, servergen.ModFile, servergen.DockerFile} {
		files[filepath.Join("server", tablesDeriverStack, goTableDeriver, file)] = filepath.Join(servergen.ServerDir(out, tablesDeriverStack, goTableDeriver), file)
	}
	for _, file := range []string{servergen.RustMainFile, servergen.RustManifestFile} {
		files[filepath.Join("server", tablesDeriverStack, rsTableDeriver, filepath.FromSlash(file))] = filepath.Join(servergen.ServerDir(out, tablesDeriverStack, rsTableDeriver), filepath.FromSlash(file))
	}
	compareGoldens(t, tablesDeriverGoldenRoot, files)

	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	goMain := read(filepath.Join(servergen.ServerDir(out, tablesDeriverStack, goTableDeriver), servergen.MainFile))
	for _, want := range []string{
		`ctx = fixturederivedprovenancedborm.WithBehaviorCaller(ctx, rules.Caller{Subject: name, Permissions: permissions})`,
		`var permissions = []string{"tickets.derive"}`,
		`deriver.TableOf("Ticket", fixtureDerivedProvenanceDb.GetTicketRepository().Derived().StaleDerivations, fixtureDerivedProvenanceDb.GetTicketRepository().Derived().SettleDerivations)`,
		`"Ticket.category": deriver.Method(methods.TicketCategory),`,
		`worker.Concurrency(8)`,
		`BatchSize:   20,`,
	} {
		if !strings.Contains(goMain, want) {
			t.Errorf("the Go deriver's main.go has no %s:\n%s", want, goMain)
		}
	}
	goAll := read(filepath.Join(servergen.ServerDir(out, tablesDeriverStack, goTableDeriverAll), servergen.MainFile))
	for _, want := range []string{`var permissions = []string{"articles.derive", "tickets.derive"}`, `"Article.summary": deriver.Method(methods.ArticleSummary),`, `worker.Concurrency(4)`, `BatchSize:   50,`} {
		if !strings.Contains(goAll, want) {
			t.Errorf("FillAll's main.go has no %s:\n%s", want, goAll)
		}
	}
	rsMain := read(filepath.Join(servergen.ServerDir(out, tablesDeriverStack, rsTableDeriver), filepath.FromSlash(servergen.RustMainFile)))
	for _, want := range []string{`const PERMISSIONS: &[&str] = &["tickets.derive"];`, `const TABLES: &[&str] = &["Ticket"];`, `.ticket()?`, `.table_operation("derived", operation, params)`, `deriver::run_deriver(`} {
		if !strings.Contains(rsMain, want) {
			t.Errorf("the Rust deriver's main.rs has no %s:\n%s", want, rsMain)
		}
	}
	for _, main := range []string{goMain, rsMain} {
		for _, unwanted := range []string{"engine", "Engine", "Article"} {
			if strings.Contains(main, unwanted) {
				t.Errorf("a deriver of the table Ticket names %s, which it opens none of:\n%s", unwanted, main)
			}
		}
	}
}

// TestAGoDeriverOfAHeldTableBuilds: a Go API's deriver of a table that
// keeps its rows to an audience (@audience, D156) builds, as one of any
// other table does (D146): held-go-api's FillNotes fills the summary of
// fixture-audience-behaviors-db's Note, and its Derivers declare the
// method. The build refused it while the Go behaviors' core read the
// table's effective audience as one UUID (D180); ormgen's
// TestHeldBehaviorTablesOnPostgres runs the deriver's loop over the table.
func TestAGoDeriverOfAHeldTableBuilds(t *testing.T) {
	const db, api = "fixture-audience-behaviors-db", "held-go-api"
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for name, root := range map[string]string{db: tsBehaviorsServicesRoot, api: tablesDeriverRoot} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(root, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	enabled := map[string]any{"enabled": true}
	f.configs[db].Outputs = map[string]any{
		"types": map[string]any{"go": enabled},
		"sql":   map[string]any{"dialects": []any{"postgres"}},
	}
	repoRoot := t.TempDir()
	f.build(t, repoRoot, tablesDeriverPaths(repoRoot), db, api)
	deps, err := os.ReadFile(filepath.Join(repoRoot, "schemas", "dist", "api", api, "deps.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FillNotes", "NoteSummary(ctx context.Context"} {
		if !strings.Contains(string(deps), want) {
			t.Errorf("held-go-api's deps.go has no %s:\n%s", want, deps)
		}
	}
}

// TestGoRustTableDeriversOfTheStack: the stack resolves each deriver of
// tables on the local target's deriver platform in its API's language, a
// process of its own, with FillAll filling every table of the database that
// implements Derived.
func TestGoRustTableDeriversOfTheStack(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTablesDeriverFixture(t)
	f.build(t, repoRoot, tablesDeriverPaths(repoRoot), tablesDeriverOrder...)
	data, err := os.ReadFile(filepath.Join(repoRoot, "schemas", "dist", "stack", tablesDeriverStack, "Local", "environment.json"))
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
			got = append(got, d.Name+" "+d.Language+" "+d.Platform+" "+d.Deriver.Database+" "+strings.Join(d.Deriver.Schemas, ","))
		}
	}
	slices.Sort(got)
	want := []string{
		"derived-go-api-fill-all GO local.deriver fixture-derived-provenance-db Article,Ticket",
		"derived-go-api-triage-tickets GO local.deriver fixture-derived-provenance-db Ticket",
		"derived-rs-api-fill-all RUST local.deriver fixture-derived-provenance-db Article,Ticket",
		"derived-rs-api-triage-tickets RUST local.deriver fixture-derived-provenance-db Ticket",
	}
	if !slices.Equal(got, want) {
		t.Errorf("derivers =\n%q\nwant\n%q", got, want)
	}
}

// TestGoRustTableDeriverEntrypointsCompile: the Go deriver's module, its
// API's and the scaffolded implementation build and vet against the
// repository's runtime modules, and the Rust deriver's crate, its API's and
// the scaffolded implementation pass clippy in the output root's
// workspace, frozen to the lockfile the stack's build wrote, with its
// main.rs laid out as rustfmt lays it out (D146).
func TestGoRustTableDeriverEntrypointsCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	repoRoot, err := filepath.EvalSymlinks(testpaths.TempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	f := loadTablesDeriverFixture(t)
	f.build(t, repoRoot, paths, tablesDeriverOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")

	t.Run("go", func(t *testing.T) {
		for _, dir := range []string{servergen.ServerDir(out, tablesDeriverStack, goTableDeriver), servergen.ServerDir(out, tablesDeriverStack, goTableDeriverAll), naming.Default().GoImplementationDir(repoRoot, goDerivedAPI)} {
			for _, args := range [][]string{{"mod", "tidy"}, {"vet", "./..."}} {
				cmd := exec.Command("go", args...)
				cmd.Dir = dir
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("go %s in %s: %v\n%s", strings.Join(args, " "), dir, err, output)
				}
			}
		}
	})

	t.Run("rust", func(t *testing.T) {
		cargo, err := exec.LookPath("cargo")
		if err != nil {
			t.Skip("cargo not available; skipping the Rust build")
		}
		target := rustTargetDir(t)
		cargoCommand(t, cargo, out, target, "clippy", "--locked", "--workspace", "--quiet", "--", "-D", "warnings")
		if rustfmt, err := exec.LookPath("rustfmt"); err == nil {
			source := filepath.Join(servergen.ServerDir(out, tablesDeriverStack, rsTableDeriver), filepath.FromSlash(servergen.RustMainFile))
			if diff, err := exec.Command(rustfmt, "--edition", "2021", "--check", source).CombinedOutput(); err != nil {
				t.Errorf("rustfmt lays out %s otherwise: %v\n%s", source, err, diff)
			}
		}
	})
}
