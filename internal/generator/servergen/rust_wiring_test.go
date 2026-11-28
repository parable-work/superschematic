package servergen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The fixture of the generated Rust wiring (#620, #622, #617, #610):
// fixture-agent-runs-db, the loader's fixture of the notifier, parties,
// the inbound path, the agent orchestration, search and grants, on
// Postgres; fixture-wired-api, a Rust API over it whose workers are the runtime
// parts' queues; and wired-stack, which deploys it on the local target.
const (
	wiredStack      = "wired-stack"
	wiredAPI        = "fixture-wired-api"
	wiredDB         = "fixture-agent-runs-db"
	wiredRoot       = "testdata/rust-wiring/services"
	wiredGoldenRoot = "testdata/golden/rust-wiring"
)

// wiredOrder is the order a build-all builds the fixture in, and
// wiredWorkers its workers' deployables.
var (
	wiredOrder   = []string{wiredDB, wiredAPI, wiredStack}
	wiredWorkers = []string{"fixture-wired-api-notify", "fixture-wired-api-dispatch", "fixture-wired-api-inbound-runs", "fixture-wired-api-index"}
)

// loadWiredFixture loads the fixture with the core registry and
// stacktest's fake target, the database's config narrowed to its Go and
// Rust types and its Postgres DDL.
func loadWiredFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range wiredOrder {
		root := tsBehaviorsServicesRoot
		if name == wiredStack {
			root = wiredRoot
		}
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(root, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	enabled := map[string]any{"enabled": true}
	f.configs[wiredDB].Outputs = map[string]any{
		"types": map[string]any{"go": enabled, "rust": enabled},
		"sql":   map[string]any{"dialects": []any{"postgres"}},
	}
	return f
}

// wiredFiles are the files of the fixture's build the goldens hold, by
// their path under wiredGoldenRoot: each entrypoint's main.rs and
// Cargo.toml, the API crate's wiring, Deps and lib.rs, and the
// implementation's scaffold.
func wiredFiles(repoRoot string) map[string]string {
	out := filepath.Join(repoRoot, "schemas", "dist")
	names := naming.Default()
	files := map[string]string{
		"api/fixture-wired-api/src/wiring.rs":                     filepath.Join(out, "api", wiredAPI, "src", rustrestgen.WiringFile),
		"api/fixture-wired-api/src/deps.rs":                       filepath.Join(out, "api", wiredAPI, "src", "deps.rs"),
		"api/fixture-wired-api/src/lib.rs":                        filepath.Join(out, "api", wiredAPI, "src", "lib.rs"),
		"rust/fixture-wired-api/" + rustrestgen.ImplementationLib: filepath.Join(names.RustImplementationDir(repoRoot, wiredAPI), rustrestgen.ImplementationLib),
	}
	for _, server := range append([]string{wiredAPI}, wiredWorkers...) {
		for _, file := range []string{servergen.RustMainFile, servergen.RustManifestFile} {
			files[filepath.Join("server", wiredStack, server, filepath.FromSlash(file))] = filepath.Join(servergen.ServerDir(out, wiredStack, server), filepath.FromSlash(file))
		}
	}
	return files
}

// TestRustWiringGolden: fixture-wired-api's server builds its database's runtime
// parts itself (#620, #622, #617, #610): it asks the implementation's
// create_wiring for the Wire that shapes them, builds Wired over the
// database's pool and the API's config, fills Deps with the notifier, the
// dispatcher and the inbound path, wraps the implementation's
// authenticator in the grants', merges the parts' routes beside the API's
// router and passes the dispatcher's mcp_gate as its MCP endpoint's call
// gate. Each worker of a part's queue builds the same Deps and runs its
// queue through Wired, with no create_workers, and the scaffold declares
// create_wiring and no method of such a worker. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestRustWiringGolden -update
func TestRustWiringGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadWiredFixture(t)
	f.build(t, repoRoot, tablesDeriverPaths(repoRoot), wiredOrder...)
	files := wiredFiles(repoRoot)
	compareGoldens(t, wiredGoldenRoot, files)

	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(files[rel])
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	server := read(filepath.Join("server", wiredStack, wiredAPI, "src", "main.rs"))
	for _, want := range []string{
		"fixture_wired_api_impl::create_wiring(&fixture_wired_api_wiring_deps)",
		"fixture_wired_api_api::Wired::new(&fixture_wired_api_wiring_deps, fixture_wired_api_wire)",
		"notifier: fixture_wired_api_wired.notifier.clone(),",
		"agents: fixture_wired_api_wired.agents.clone(),",
		"inbound: fixture_wired_api_wired.inbound.clone(),",
		".handlers(fixture_wired_api_handlers)",
		"fixture_wired_api_wired.routes(&fixture_wired_api_implementations)",
		".merge(fixture_wired_api_wired_routes)",
		".with_call_gate(fixture_wired_api_wired.mcp_gate())",
	} {
		if !strings.Contains(server, want) {
			t.Errorf("the server's main.rs has no %s:\n%s", want, server)
		}
	}
	for worker, run := range map[string]string{
		"fixture-wired-api-notify":       ".run_notice_work(options, shutdown)",
		"fixture-wired-api-dispatch":     ".run_agent_run_request(options, shutdown)",
		"fixture-wired-api-inbound-runs": ".run_run_request(options, shutdown)",
		"fixture-wired-api-index":        ".run_search_indexing(options, shutdown)",
	} {
		main := read(filepath.Join("server", wiredStack, worker, "src", "main.rs"))
		if !strings.Contains(main, run) || strings.Contains(main, "create_workers") || strings.Contains(main, "PostgresQueue") {
			t.Errorf("%s's main.rs does not run its queue through Wired alone:\n%s", worker, main)
		}
	}
	scaffold := read("rust/fixture-wired-api/" + rustrestgen.ImplementationLib)
	if !strings.Contains(scaffold, "pub async fn create_wiring(") || strings.Contains(scaffold, "async fn notify(") {
		t.Errorf("the scaffold declares no create_wiring, or a method of a part's worker:\n%s", scaffold)
	}
}

// TestARustImplementationThatPredatesItsWiringFails: an implementation of
// fixture-wired-api that exists and declares no create_wiring, which every
// entrypoint of the API builds its runtime parts with (#620), is refused by
// the build, saying what to add; one that declares it builds.
func TestARustImplementationThatPredatesItsWiringFails(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadWiredFixture(t)
	paths := tablesDeriverPaths(repoRoot)
	f.build(t, repoRoot, paths, wiredOrder...)
	lib := filepath.Join(naming.Default().RustImplementationDir(repoRoot, wiredAPI), rustrestgen.ImplementationLib)
	scaffold, err := os.ReadFile(lib)
	if err != nil {
		t.Fatal(err)
	}
	stale := strings.Replace(string(scaffold), "pub async fn create_wiring(", "pub async fn old_wiring(", 1)
	if err := os.WriteFile(lib, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = generator.Run(f.schemas[wiredStack], f.configs[wiredStack], f.options(repoRoot, paths))
	for _, want := range []string{"declares no create_wiring", "pub async fn create_wiring(deps: &WiringDeps) -> Result<Arc<dyn Wire>, BoxError>", "impl Wire for Wiring {}"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("the build answered %v, want a refusal that says %q", err, want)
		}
	}
	if err := os.WriteFile(lib, scaffold, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := generator.Run(f.schemas[wiredStack], f.configs[wiredStack], f.options(repoRoot, paths)); err != nil {
		t.Fatalf("the build of an implementation with create_wiring: %v", err)
	}
}
