package tsrestgen

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// blueprintAPI is the API that serves fixture-blueprint-db's Batch and Job
// (D149) for any signed-in caller: Batch's Workflow, whose create stamps a
// job per step of its Blueprint template, and Job's Workflow and blockers.
// The router serves its MCP tools.
const blueprintAPI = "fixture-blueprint-api"

// blueprintDB is fixture-blueprint-api's database, its one DB dependency.
const blueprintDB = "fixture-blueprint-db"

// blueprintFixture is fixture-blueprint-api with its database and apigen's
// endpoints for it, read with one registry, which declares the behaviors.
func blueprintFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, blueprintDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", blueprintDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, blueprintAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", blueprintAPI, err)
	}
	deps := map[string]*ir.Schema{blueprintDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     blueprintAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: blueprintAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, mcp: true, depsInfo: blueprintDeps}
}

// blueprintDeps is fixture-blueprint-api's Deps as the dispatch layer sets
// it: a pool of its database, which lists postgres first, and the
// repositories its TypeScript types export.
var blueprintDeps = DepsInfo{
	Database:             blueprintDB,
	Dialect:              "postgres",
	ORM:                  "FixtureBlueprintDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-blueprint-db-types",
}

// TestWriteAPIGoldenBehaviorBlueprint pins the package for
// fixture-blueprint-api: its router mounts Batch's transition and Job's
// transition, addBlocker, removeBlocker and listBlockers, each calling the
// repository's operation under its member, Blueprint, which declares no
// operation, none, and no behavior worker. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenBehaviorBlueprint -update
func TestWriteAPIGoldenBehaviorBlueprint(t *testing.T) {
	fixture := blueprintFixture(t)
	output := generateBehavior(t, fixture)
	if err := output.SetDeps(blueprintDeps); err != nil {
		t.Fatalf("SetDeps: %v", err)
	}
	var paths []string
	for _, ep := range fixture.endpoints.Endpoints {
		if ep.BehaviorOperation() != nil {
			paths = append(paths, ep.Method+" "+ep.Path)
		}
	}
	slices.Sort(paths)
	if want := []string{
		"POST /api/batch/{id}/status/transition",
		"POST /api/job/{id}/dependencies/add-blocker",
		"POST /api/job/{id}/dependencies/list-blockers",
		"POST /api/job/{id}/dependencies/remove-blocker",
		"POST /api/job/{id}/status/transition",
	}; !slices.Equal(paths, want) {
		t.Errorf("the behavior routes = %q, want %q", paths, want)
	}
	checkGolden(t, output, blueprintAPI)
}
