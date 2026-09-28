package apigen_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/ormgen"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestPublicAPIOverAVersionGraphUpstreamCompiles generates a public API
// whose upstream DB schema declares a version graph. The upstream ORM
// imports the version-graph core's Go binding, and a replace directive in
// the ORM's go.mod does not reach a module that imports the ORM, so the
// API's go.mod must carry the binding's replace itself. The test writes the
// upstream types and ORM and the API into a temp tree laid out as a build
// writes it, then tidies, builds and vets the API module, which links the
// binding through the ORM.
func TestPublicAPIOverAVersionGraphUpstreamCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	const upstream = "fixture-version-graph-db"
	paths := testpaths.Local(t)
	root := t.TempDir()

	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, upstream))
	if err != nil {
		t.Fatalf("load %s: %v", upstream, err)
	}
	dbTypesModule := "example.com/schemas/types/go/" + upstream
	dbTypesDir := filepath.Join(root, "types", "go", upstream)
	dbTypes, err := typegen.Generate(dbSchema, typegen.Options{SchemaName: upstream, ModulePath: dbTypesModule, Clock: goModuleClock})
	if err != nil {
		t.Fatalf("typegen.Generate %s: %v", upstream, err)
	}
	if err := typegen.SetReplacePaths(dbTypes, paths, dbTypesDir); err != nil {
		t.Fatalf("typegen.SetReplacePaths %s: %v", upstream, err)
	}
	if err := typegen.WriteTypes(dbTypes, dbTypesDir); err != nil {
		t.Fatalf("typegen.WriteTypes %s: %v", upstream, err)
	}
	ormDir := filepath.Join(root, "orm", upstream)
	orm, err := ormgen.Generate(dbSchema, ormgen.Options{
		SchemaName:  upstream,
		ModulePath:  "example.com/schemas/orm/" + upstream,
		TypesModule: dbTypesModule,
		Clock:       goModuleClock,
	})
	if err != nil {
		t.Fatalf("ormgen.Generate: %v", err)
	}
	if err := ormgen.SetReplacePaths(orm, paths, ormDir); err != nil {
		t.Fatalf("ormgen.SetReplacePaths: %v", err)
	}
	if err := ormgen.WriteORM(orm, ormDir); err != nil {
		t.Fatalf("ormgen.WriteORM: %v", err)
	}

	apiSchema := loadBodyArgsAPI(t)
	apiTypesModule := "example.com/schemas/types/go/" + bodyArgsAPI
	apiTypesDir := filepath.Join(root, "types", "go", bodyArgsAPI)
	apiTypes, err := typegen.Generate(apiSchema, typegen.Options{SchemaName: bodyArgsAPI, ModulePath: apiTypesModule, Clock: goModuleClock})
	if err != nil {
		t.Fatalf("typegen.Generate %s: %v", bodyArgsAPI, err)
	}
	if err := typegen.SetReplacePaths(apiTypes, paths, apiTypesDir); err != nil {
		t.Fatalf("typegen.SetReplacePaths %s: %v", bodyArgsAPI, err)
	}
	if err := typegen.WriteTypes(apiTypes, apiTypesDir); err != nil {
		t.Fatalf("typegen.WriteTypes %s: %v", bodyArgsAPI, err)
	}
	apiDir := filepath.Join(root, "api", bodyArgsAPI)
	api, err := apigen.Generate(apiSchema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     bodyArgsAPI,
		ModulePath:     "example.com/schemas/api/" + bodyArgsAPI,
		TypesModule:    apiTypesModule,
		IsPublic:       true,
		UpstreamSchema: upstream,
		UpstreamIR:     dbSchema,
		Clock:          goModuleClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	if err := apigen.SetReplacePaths(api, paths, apiDir); err != nil {
		t.Fatalf("apigen.SetReplacePaths: %v", err)
	}
	if err := apigen.WriteAPI(api, apiDir); err != nil {
		t.Fatalf("apigen.WriteAPI: %v", err)
	}

	for _, args := range [][]string{{"mod", "tidy"}, {"build", "./..."}, {"vet", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}
