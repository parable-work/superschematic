package tsrestgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// audienceQueueDeps is fixture-audience-queue-api's Deps as the dispatch
// layer sets it: the pages' @audience queue among the runtime's (#734).
var audienceQueueDeps = DepsInfo{
	Database:             "fixture-audience-queue-db",
	Dialect:              "postgres",
	ORM:                  "FixtureAudienceQueueDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-audience-queue-db-types",
	Routes:               DepsRoutes{AudienceQueues: []string{"AudienceResolving"}},
}

// TestAudienceQueueWorkerIsTheRuntimes: a @worker of an @audience
// declaration's queue (D156, #734) is the runtime's: the API package's
// audience.ts exports resolveAudienceMessage, which hands each message to
// resolveAudience over Deps.orm, the index exports it, the Workers
// interface has no method for the worker, and the package type-checks.
func TestAudienceQueueWorkerIsTheRuntimes(t *testing.T) {
	const api, db = "fixture-audience-queue-api", "fixture-audience-queue-db"
	reg := registry.New(naming.Default())
	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, db), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", db, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, api), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", api, err)
	}
	deps := map[string]*ir.Schema{db: dbSchema}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     api,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	output, err := Generate(schema, endpoints, Options{SchemaName: api, Dependencies: deps, Clock: fixedClock})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := output.SetDeps(audienceQueueDeps); err != nil {
		t.Fatalf("SetDeps: %v", err)
	}
	if workers := output.RuntimeWorkers(WorkerAudience); len(workers) != 1 || workers[0].Name != "ResolveAudience" {
		t.Fatalf("the runtime's audience workers are %+v, want ResolveAudience", workers)
	}
	if !output.HasAudienceQueues() || output.HasRuntime() {
		t.Errorf("HasAudienceQueues %v, HasRuntime %v: audience.ts alone holds the queue's handler", output.HasAudienceQueues(), output.HasRuntime())
	}
	fixture := apiFixture{name: api, schema: schema, deps: deps, endpoints: endpoints, registry: reg, depsInfo: audienceQueueDeps}
	tree := materializeAPI(t, fixture)
	audience, err := os.ReadFile(filepath.Join(tree.apiDir, AudienceFile))
	if err != nil {
		t.Fatal(err)
	}
	if want := "await resolveAudience(deps.orm, message as AudienceResolving);"; !strings.Contains(string(audience), want) {
		t.Errorf("%s lacks %s:\n%s", AudienceFile, want, audience)
	}
	index, err := os.ReadFile(filepath.Join(tree.apiDir, "index.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "export * from './audience';") {
		t.Errorf("index.ts does not export audience.ts:\n%s", index)
	}
	interfaces, err := os.ReadFile(filepath.Join(tree.apiDir, "interfaces.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(interfaces), "resolveAudience(") {
		t.Errorf("the Workers interface has a method for the audience queue's worker:\n%s", interfaces)
	}
	tree.typeCheck(t)
}
