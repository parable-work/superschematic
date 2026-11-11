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

// The TypeScript table operations fixture (D106): fixture-tracker-api,
// whose operations but one are marked @tableOperation and served from the
// tables of fixture-tracker-db, built as a TypeScript API, and
// tracker-ts-stack, which serves it from its default server on the local
// target.
const (
	// tsTablesStackRoot holds tracker-ts-stack.
	tsTablesStackRoot = "testdata/tables/typescript/services"
	tsTablesStack     = "tracker-ts-stack"
	tsTablesServer    = "fixture-tracker-api"
	tsTablesDB        = "fixture-tracker-db"
	// tsTablesGoldenRoot holds the server's files as the output root lays
	// them out.
	tsTablesGoldenRoot = "testdata/golden/tables/typescript"
)

// tsTablesOrder is the order a build-all builds the fixture in.
var tsTablesOrder = []string{tsTablesDB, tsTablesServer, tsTablesStack}

// loadTypeScriptTablesFixture loads the fixture with the core registry and
// stacktest's fake target. The API and its database are the loader's
// fixtures, whose configs it narrows to what the TypeScript server reads:
// the database's TypeScript types and DDL, with the Go types a DB service
// always generates, and the API's TypeScript types and server; edit
// changes them further when set.
func loadTypeScriptTablesFixture(t *testing.T, edit func(configs map[string]*schemaconfig.SchemaConfig)) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range tsTablesOrder {
		root := tsBehaviorsServicesRoot
		if name == tsTablesStack {
			root = tsTablesStackRoot
		}
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(root, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	enabled := map[string]any{"enabled": true}
	f.configs[tsTablesDB].Outputs = map[string]any{
		"types": map[string]any{"go": enabled, "typescript": enabled},
		"sql":   map[string]any{"dialects": []any{"postgres"}},
	}
	f.configs[tsTablesServer].Outputs = map[string]any{
		"types": map[string]any{"typescript": enabled},
		"api":   map[string]any{"enabled": true, "language": "TYPESCRIPT"},
	}
	if edit != nil {
		edit(f.configs)
	}
	return f
}

// TestTypeScriptTableOperationsServerGolden: the TypeScript server of
// fixture-tracker-api hands its router the repositories Deps holds, which
// run the table operations (D106), beside the implementation, which
// writes the one operation that is none. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestTypeScriptTableOperationsServerGolden -update
func TestTypeScriptTableOperationsServerGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptTablesFixture(t, nil)
	f.build(t, repoRoot, tsFakePaths(repoRoot), tsTablesOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	dir := servergen.ServerDir(out, tsTablesStack, tsTablesServer)
	files := map[string]string{}
	for _, file := range []string{servergen.TypeScriptMainFile, servergen.TypeScriptPackageFile} {
		files[filepath.Join("server", tsTablesStack, tsTablesServer, file)] = filepath.Join(dir, file)
	}
	compareGoldens(t, tsTablesGoldenRoot, files)

	main, err := os.ReadFile(filepath.Join(dir, servergen.TypeScriptMainFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"orm: fixtureTrackerApiDeps.orm", "createFixtureTrackerDbOrm("} {
		if !strings.Contains(string(main), want) {
			t.Errorf("main.ts has no %s: it hands the router no repositories:\n%s", want, main)
		}
	}
}
