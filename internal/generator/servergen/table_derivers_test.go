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
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The table deriver fixture (D146): fixture-derived-provenance-db, a
// database whose tables Ticket and Article implement Derived;
// derived-ts-api, a TypeScript API over it that declares TriageTickets,
// which fills Ticket, and FillAll, which names no schema; and
// derived-ts-stack, which deploys it on the local target.
const (
	tableDeriverStack = "derived-ts-stack"
	tableDeriverAPI   = "derived-ts-api"
	tableDeriverDB    = "fixture-derived-provenance-db"
	tableDeriver      = "derived-ts-api-triage-tickets"
	tableDeriverAll   = "derived-ts-api-fill-all"
	// tableDeriverGoldenRoot holds the deriver's files as the output root
	// lays them out.
	tableDeriverGoldenRoot = "testdata/golden/table-derivers"
)

// tableDeriverOrder is the order a build-all builds the fixture in.
var tableDeriverOrder = []string{tableDeriverDB, tableDeriverAPI, tableDeriverStack}

// loadTableDeriverFixture loads the fixture with the core registry and
// stacktest's fake target. The database is the loader's fixture, whose
// config it narrows to what the TypeScript deriver reads: its TypeScript
// types and DDL, with the Go types a DB service always generates.
func loadTableDeriverFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range tableDeriverOrder {
		root := tsBehaviorsStackRoot
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
		"types": map[string]any{"go": enabled, "typescript": enabled},
		"sql":   map[string]any{"dialects": []any{"postgres", "sqlite"}},
	}
	return f
}

// TestTableDeriverEntrypointGolden: a deriver of tables that implement
// Derived (D146). The API's Deps declares Derivers as it does over an
// engine database, a member per deriver with a method per derived field
// of its tables, typed by the field through the database's types, and the
// scaffold builds them. Each deriver is an entrypoint of its own, which
// opens no engine: it opens the database's repositories as its
// deployable's name, holding each Derived config's permission, and runs
// the runtime's runDeriver over each table's derived operations, handing
// each stale field to its method. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestTableDeriverEntrypointGolden -update
func TestTableDeriverEntrypointGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTableDeriverFixture(t)
	f.build(t, repoRoot, tsBehaviorsFakePaths(repoRoot), tableDeriverOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	names := naming.Default()
	files := map[string]string{
		"api/derived-ts-api/deps.ts":         filepath.Join(out, "api", tableDeriverAPI, "deps.ts"),
		"typescript/derived-ts-api/index.ts": filepath.Join(names.TypeScriptImplementationDir(repoRoot, tableDeriverAPI), "index.ts"),
	}
	dir := servergen.ServerDir(out, tableDeriverStack, tableDeriver)
	for _, file := range []string{servergen.TypeScriptMainFile, servergen.TypeScriptPackageFile} {
		files[filepath.Join("server", tableDeriverStack, tableDeriver, file)] = filepath.Join(dir, file)
	}
	compareGoldens(t, tableDeriverGoldenRoot, files)

	read := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	main := read(filepath.Join(dir, servergen.TypeScriptMainFile))
	for _, want := range []string{
		"import { runDeriver, type DerivationAnswer, type DerivationItem, type DerivedTable } from '@superschematic/http-runtime/deriver';",
		"createFixtureDerivedProvenanceDbOrm(fixtureDerivedProvenanceDbPool).withBehaviorCaller({",
		"subject: 'derived-ts-api-triage-tickets',",
		"permissions: ['tickets.derive'],",
		"Ticket: deriving.ticket.derived,",
		"case 'Ticket.category':",
		"return deriver.ticketPriority(derived, signal);",
		"workerConcurrency(8)",
		"batchSize: 20,",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("main.ts has no %s:\n%s", want, main)
		}
	}
	for _, unwanted := range []string{"openEngine", "inProcess(", "@superschematic/engine", "Article"} {
		if strings.Contains(main, unwanted) {
			t.Errorf("main.ts has %s, which a deriver of the table Ticket has none of:\n%s", unwanted, main)
		}
	}
	// FillAll names no schema: it fills every table that implements
	// Derived, each under its own permission.
	all := read(filepath.Join(servergen.ServerDir(out, tableDeriverStack, tableDeriverAll), servergen.TypeScriptMainFile))
	for _, want := range []string{
		"permissions: ['articles.derive', 'tickets.derive'],",
		"Article: deriving.article.derived,",
		"Ticket: deriving.ticket.derived,",
		"return deriver.articleSummary(derived, signal);",
		"workerConcurrency(4)",
		"batchSize: 50,",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("FillAll's main.ts has no %s:\n%s", want, all)
		}
	}
	// Its package depends on the behaviors' core, which orm.ts imports, and
	// on no engine.
	pkg := read(filepath.Join(dir, servergen.TypeScriptPackageFile))
	if !strings.Contains(pkg, `"@superschematic/rules": "*"`) {
		t.Errorf("the package does not depend on the behaviors' core:\n%s", pkg)
	}
	if strings.Contains(pkg, "@superschematic/engine") {
		t.Errorf("the package depends on the engine, which a deriver of tables opens none of:\n%s", pkg)
	}
}

// TestTableDeriversOfTheStack: the stack resolves each deriver of tables on
// the local target's deriver platform, a process of its own, with FillAll
// filling every table of the database that implements Derived.
func TestTableDeriversOfTheStack(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTableDeriverFixture(t)
	f.build(t, repoRoot, tsBehaviorsFakePaths(repoRoot), tableDeriverOrder...)
	data, err := os.ReadFile(filepath.Join(repoRoot, "schemas", "dist", "stack", tableDeriverStack, "Local", "environment.json"))
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
		"derived-ts-api-fill-all TYPESCRIPT local.deriver fixture-derived-provenance-db Article,Ticket",
		"derived-ts-api-triage-tickets TYPESCRIPT local.deriver fixture-derived-provenance-db Ticket",
	}
	if !slices.Equal(got, want) {
		t.Errorf("derivers =\n%q\nwant\n%q", got, want)
	}
	// No engine runs over a database of tables, so the stack has no runner.
	for _, d := range env.Deployables {
		if d.Kind == ir.DeployableRunner {
			t.Errorf("the stack runs %s, an engine's runner, over a database with no engine type", d.Name)
		}
	}
}

// TestTableDeriverRefusals: the build refuses a deriver of a table that
// implements no Derived and a deriver of tables whose database lists
// sqlite first, which has no TypeScript repositories, and builds a Rust
// API's deriver of tables.
func TestTableDeriverRefusals(t *testing.T) {
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
	enabled := map[string]any{"enabled": true}

	f := loadTableDeriverFixture(t)
	f.schemas[tableDeriverAPI].Derivers[0].Schemas = []string{"Note"}
	if err := build(f, tableDeriverDB, tableDeriverAPI); err == nil || !strings.Contains(err.Error(), "deriver TriageTickets of derived-ts-api fills the derived fields of Note, which is no engine type or table of fixture-derived-provenance-db that composes Derived (its types that do: Article, Ticket)") {
		t.Errorf("build = %v, want a schema without Derived refused", err)
	}

	f = loadTableDeriverFixture(t)
	rustAPI := *f.configs[tableDeriverAPI]
	rustAPI.Outputs = map[string]any{"types": map[string]any{"rust": enabled}, "api": map[string]any{"enabled": true, "language": "RUST"}}
	f.configs[tableDeriverAPI] = &rustAPI
	dbConfig := *f.configs[tableDeriverDB]
	dbConfig.Outputs = map[string]any{"types": map[string]any{"go": enabled, "typescript": enabled, "rust": enabled}, "sql": map[string]any{"dialects": []any{"postgres"}}}
	f.configs[tableDeriverDB] = &dbConfig
	if err := build(f, tableDeriverDB, tableDeriverAPI); err != nil {
		t.Errorf("build = %v, want a Rust API's deriver of tables built (TestGoRustTableDeriverEntrypointsGolden holds its files)", err)
	}

	f = loadTableDeriverFixture(t)
	sqliteFirst := *f.configs[tableDeriverDB]
	sqliteFirst.Outputs = map[string]any{"types": map[string]any{"go": enabled, "typescript": enabled}, "sql": map[string]any{"dialects": []any{"sqlite", "postgres"}}}
	f.configs[tableDeriverDB] = &sqliteFirst
	if err := build(f, tableDeriverOrder...); err == nil || !strings.Contains(err.Error(), "fills the Derived fields of") || !strings.Contains(err.Error(), "through the repositories fixture-derived-provenance-db's TypeScript types export for Postgres (D146)") {
		t.Errorf("build = %v, want a deriver of tables over a sqlite-first database refused", err)
	}
}

// TestTableDeriverEntrypointCompiles: the deriver's package and the
// scaffolded implementation install in the output root's workspace with
// the real runtime, the core's package and the generated packages, and
// type-check: each table's derived operations are a DerivedTable, and each
// method takes its field's inputs and answers its field's type (D146).
func TestTableDeriverEntrypointCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	installTypeScriptRuntime(t, bun, paths)
	testpaths.BuildRulesPackage(t, bun, paths)
	repoRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := loadTableDeriverFixture(t)
	f.build(t, repoRoot, paths, tableDeriverOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	install := exec.Command(bun, "install")
	install.Dir = out
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
	for _, checked := range []string{servergen.ServerDir(out, tableDeriverStack, tableDeriver), servergen.ServerDir(out, tableDeriverStack, tableDeriverAll), naming.Default().TypeScriptImplementationDir(repoRoot, tableDeriverAPI)} {
		check := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
		check.Dir = checked
		if output, err := check.CombinedOutput(); err != nil {
			t.Fatalf("tsc in %s: %v\n%s", checked, err, output)
		}
	}
}
