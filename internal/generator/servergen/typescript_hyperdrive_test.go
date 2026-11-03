package servergen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The Hyperdrive fixture (D92): ts-ledger, a finance-like TypeScript API
// over ts-ledger-db, a Postgres database with a vector column and two keyed
// queues, deployed by ts-ledger-stack, whose Edge environment places the
// server on stacktest's fake bundle platform, as Workers, each of its two
// workers on the fake drain platform, as a Worker under a Cron Trigger, and
// the database on the fake Postgres platform, which each reaches through a
// Hyperdrive binding.
var tsLedgerOrder = []string{"ts-ledger-db", "ts-ledger", "ts-ledger-stack"}

// The deployables of ts-ledger's workers: EmbedLineItems, which drains the
// queue LineItem's trigger fills, and SyncItems, which drains the keyed and
// serialized SyncRequested.
const (
	tsEmbedLineItems = "ts-ledger-embed-line-items"
	tsSyncItems      = "ts-ledger-sync-items"
)

// loadTypeScriptLedgerFixture loads the Hyperdrive fixture's services.
func loadTypeScriptLedgerFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range tsLedgerOrder {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsServicesRoot, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	return f
}

// tsLedgerGenerated lists the files of the Hyperdrive fixture the goldens
// hold, by their path under tsGoldenRoot: the server's fetch.ts and
// bundle.ts, and each worker's drain.ts, and the sync worker's package.json.
func tsLedgerGenerated(repoRoot string) map[string]string {
	out := filepath.Join(repoRoot, "schemas", "dist")
	files := map[string]string{}
	add := func(server, file string) {
		files[filepath.Join("server", "ts-ledger-stack", server, file)] = filepath.Join(servergen.ServerDir(out, "ts-ledger-stack", server), file)
	}
	add("ts-ledger", servergen.TypeScriptFetchFile)
	add("ts-ledger", servergen.TypeScriptBundleFile)
	add(tsEmbedLineItems, servergen.TypeScriptDrainFile)
	add(tsSyncItems, servergen.TypeScriptDrainFile)
	add(tsSyncItems, servergen.TypeScriptPackageFile)
	add(tsSyncItems, servergen.TypeScriptConfigFile)
	return files
}

// TestTypeScriptHyperdriveEntrypointGolden: ts-ledger-db lists postgres
// alone, and Edge places ts-ledger's server and both its workers on bundle
// platforms (D92). The server's fetch.ts opens Deps.db over the hyperdrive
// binding its connection names, Deps.orm over it, pings it at /readyz and
// runs each request in a scope of its own; each worker's package gets
// drain.ts, which claims its queue from Postgres for a window at each
// firing, beside main.ts, and its tsconfig.json type-checks drain.ts; each
// bundle.ts keeps Node's modules external. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestTypeScriptHyperdriveEntrypointGolden -update
func TestTypeScriptHyperdriveEntrypointGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptLedgerFixture(t)
	f.build(t, repoRoot, tsFakePaths(repoRoot), tsLedgerOrder...)
	compareGoldens(t, tsGoldenRoot, tsLedgerGenerated(repoRoot))
	out := filepath.Join(repoRoot, "schemas", "dist")
	for _, worker := range []string{tsEmbedLineItems, tsSyncItems} {
		dir := servergen.ServerDir(out, "ts-ledger-stack", worker)
		for _, file := range []string{servergen.TypeScriptMainFile, servergen.TypeScriptBundleFile, servergen.TypeScriptWorkersTypesFile} {
			if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
				t.Errorf("worker %s's package has no %s: %v", worker, file, err)
			}
		}
		for _, file := range []string{servergen.TypeScriptFetchFile, servergen.TypeScriptScheduledFile} {
			if _, err := os.Stat(filepath.Join(dir, file)); err == nil {
				t.Errorf("worker %s's package has %s", worker, file)
			}
		}
		bundle, err := os.ReadFile(filepath.Join(dir, servergen.TypeScriptBundleFile))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(bundle), "const ENTRY = 'drain.ts';") || !strings.Contains(string(bundle), "plugins: [nodeModules]") {
			t.Errorf("worker %s's bundle.ts does not bundle drain.ts with Node's modules:\n%s", worker, bundle)
		}
	}
}
