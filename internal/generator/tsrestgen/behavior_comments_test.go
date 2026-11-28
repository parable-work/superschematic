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

// commentsAPI is the API that serves fixture-comments-db's Ticket (D103's
// step 4) for any signed-in caller: each of Comments' operations a table
// runs, a route of the row under comments. The router serves its MCP
// tools.
const commentsAPI = "fixture-comments-api"

// commentsDB is fixture-comments-api's database, its one DB dependency.
const commentsDB = "fixture-comments-db"

// commentsFixture is fixture-comments-api with its database and apigen's
// endpoints for it, read with one registry, which declares the behaviors.
func commentsFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, commentsDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", commentsDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, commentsAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", commentsAPI, err)
	}
	deps := map[string]*ir.Schema{commentsDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     commentsAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: commentsAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, mcp: true, depsInfo: commentsDeps}
}

// commentsDeps is fixture-comments-api's Deps as the dispatch layer sets
// it: a pool of its database, which lists postgres first, and the
// repositories its TypeScript types export.
var commentsDeps = DepsInfo{
	Database:             commentsDB,
	Dialect:              "postgres",
	ORM:                  "FixtureCommentsDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-comments-db-types",
}

// TestWriteAPIGoldenBehaviorComments pins the package for
// fixture-comments-api: its router mounts each of Ticket's Comments'
// operations under comments, each calling the repository's operation under
// that member, every writing one beside the row, and no behavior worker.
// Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenBehaviorComments -update
func TestWriteAPIGoldenBehaviorComments(t *testing.T) {
	fixture := commentsFixture(t)
	output := generateBehavior(t, fixture)
	if err := output.SetDeps(commentsDeps); err != nil {
		t.Fatalf("SetDeps: %v", err)
	}
	var paths []string
	for _, ep := range fixture.endpoints.Endpoints {
		if ep.BehaviorOperation() != nil {
			paths = append(paths, ep.Method+" "+ep.Path)
		}
	}
	slices.Sort(paths)
	var want []string
	for _, name := range []string{"delete-message", "edit", "follow", "get-message", "list-edits", "list-messages", "list-pinned", "list-reactions", "mark-read", "pin", "post", "purge", "react", "reopen", "resolve", "unpin", "unreact"} {
		want = append(want, "POST /api/ticket/{id}/comments/"+name)
	}
	if !slices.Equal(paths, want) {
		t.Errorf("the behavior routes = %q, want %q", paths, want)
	}
	checkGolden(t, output, commentsAPI)
}
