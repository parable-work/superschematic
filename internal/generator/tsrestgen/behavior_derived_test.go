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

// derivedProvenanceAPI is the API that serves
// fixture-derived-provenance-db's Ticket and Article (D73 on D103's step
// 4) for any signed-in caller: Derived's pulls and settles, routes of the
// table as a whole, and its applyDerived and rederive, Provenance's accept
// and reject and the status Workflow's transition, routes of the row under
// each use's member. The router serves its MCP tools.
const derivedProvenanceAPI = "fixture-derived-provenance-api"

// derivedProvenanceDB is fixture-derived-provenance-api's database, its
// one DB dependency.
const derivedProvenanceDB = "fixture-derived-provenance-db"

// derivedProvenanceFixture is fixture-derived-provenance-api with its
// database and apigen's endpoints for it, read with one registry, which
// declares the behaviors.
func derivedProvenanceFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, derivedProvenanceDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", derivedProvenanceDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, derivedProvenanceAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", derivedProvenanceAPI, err)
	}
	deps := map[string]*ir.Schema{derivedProvenanceDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     derivedProvenanceAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: derivedProvenanceAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, mcp: true, depsInfo: derivedProvenanceDeps}
}

// derivedProvenanceDeps is fixture-derived-provenance-api's Deps as the
// dispatch layer sets it: a pool of its database, which lists postgres
// first, and the repositories its TypeScript types export.
var derivedProvenanceDeps = DepsInfo{
	Database:             derivedProvenanceDB,
	Dialect:              "postgres",
	ORM:                  "FixtureDerivedProvenanceDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-derived-provenance-db-types",
}

// TestWriteAPIGoldenBehaviorDerivedProvenance pins the package for
// fixture-derived-provenance-api: its router mounts Derived's pulls and
// settles on the table as a whole, each calling the repository's operation
// that takes no key, and the rest on the row under each use's member, and
// no behavior worker. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenBehaviorDerivedProvenance -update
func TestWriteAPIGoldenBehaviorDerivedProvenance(t *testing.T) {
	fixture := derivedProvenanceFixture(t)
	output := generateBehavior(t, fixture)
	if err := output.SetDeps(derivedProvenanceDeps); err != nil {
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
		"POST /api/article/derived/failed-derivations",
		"POST /api/article/derived/settle-derivations",
		"POST /api/article/derived/stale-derivations",
		"POST /api/article/{id}/derived/apply-derived",
		"POST /api/article/{id}/derived/rederive",
		"POST /api/ticket/derived/failed-derivations",
		"POST /api/ticket/derived/settle-derivations",
		"POST /api/ticket/derived/stale-derivations",
		"POST /api/ticket/{id}/derived/apply-derived",
		"POST /api/ticket/{id}/derived/rederive",
		"POST /api/ticket/{id}/provenance/accept",
		"POST /api/ticket/{id}/provenance/reject",
		"POST /api/ticket/{id}/status/transition",
	}; !slices.Equal(paths, want) {
		t.Errorf("the behavior routes = %q, want %q", paths, want)
	}
	checkGolden(t, output, derivedProvenanceAPI)
}
