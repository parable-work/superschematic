package apigen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
)

// poolFixtures are the fixtures of Pool (D104): fixture-pool-api serves the
// operations of fixture-pool-db's Account, which implements Pool and
// Budget, from a @behaviorOperations set, pick among them an operation of
// the table as a whole, and declares the behavior worker AccountRechecks,
// which runs Account's deadlines and events, each recheck event handed to
// the project's Workers method AccountRechecksPoolRecheck.
var poolFixtures = behaviorFixtures{"fixture-pool-api", "fixture-pool-db"}

// setPoolDeps sets what the dispatch layer gives an API with workers beside
// its database: the database's Go types module, which declares the event
// queue's message, and the name the package imports it as.
func setPoolDeps(output *apigen.APIOutput) {
	output.Deps.DatabaseTypes = "example.com/schemas/types/go/" + poolFixtures.db
	output.Deps.DatabaseTypesAlias = "fixturepooldb"
}

// TestWriteAPIGoldenPool generates the Go API module of fixture-pool-api
// and holds the files Account's Pool and Budget change to their golden
// copies: the routes, whose handler of pick, routed with no key, runs it
// with runTableBehaviorOperation as a command without a row (D104), and
// whose handlers of markOut, reportRoom and Budget's operations run them
// with runBehaviorOperation; behaviors.go, which holds both and the queues
// of AccountRechecks, its deadlines and events, each event handed to the
// Workers method of its use and kind; deps.go, whose Workers holds that
// method, the worker's one event; the interfaces; the OpenAPI document; and
// the permission catalog. Regenerate with:
// go test ./internal/generator/apigen -run TestWriteAPIGoldenPool -update
func TestWriteAPIGoldenPool(t *testing.T) {
	api, db := loadFixturePair(t, poolFixtures)
	output := generatePairAPI(t, poolFixtures, api, db)
	setPoolDeps(output)

	var calls []string
	for _, ep := range output.Endpoints {
		bo := ep.BehaviorOperation()
		if bo == nil || bo.Use != "Pool" {
			continue
		}
		calls = append(calls, ep.Method+" "+ep.Path+" "+ep.BehaviorRepositoryCall()+" "+ep.BehaviorRunner())
	}
	slices.Sort(calls)
	want := []string{
		"POST /api/account/pool/pick GetAccountRepository().Pool().Pick runTableBehaviorOperation",
		"POST /api/account/{id}/pool/mark-out GetAccountRepository().Pool().MarkOut runBehaviorOperation",
		"POST /api/account/{id}/pool/report-room GetAccountRepository().Pool().ReportRoom runBehaviorOperation",
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("Pool's endpoints = %q, want %q", calls, want)
	}
	if !output.HasTableBehaviorEndpoints() {
		t.Fatal("HasTableBehaviorEndpoints is false")
	}
	if len(output.Workers) != 1 || output.Workers[0].Behavior == nil {
		t.Fatalf("workers = %+v, want the behavior worker AccountRechecks", output.Workers)
	}
	w := output.Workers[0]
	if got := []string{w.Behavior.DeadlineQueue, w.Behavior.ChangeQueue, w.Behavior.EventQueue}; !slices.Equal(got, []string{"AccountDeadline", "", "AccountEvent"}) || !w.Behavior.Emits() {
		t.Fatalf("AccountRechecks' queues = %q, want its deadline and event queues", got)
	}
	events := output.WorkerEvents()
	if len(events) != 1 || events[0].Method != "AccountRechecksPoolRecheck" || events[0].Event != "AccountPoolRecheckEvent" || events[0].Queue != "AccountEvent" {
		t.Fatalf("the worker's events = %+v, want recheck of Pool, handed to AccountRechecksPoolRecheck", events)
	}

	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
		RulesGo:         "/repo/runtime/rules/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+poolFixtures.api); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	goldenDir := filepath.Join("testdata", "golden", poolFixtures.api)
	for _, name := range []string{"go.mod", "routes.go", "interfaces.go", "behaviors.go", "deps.go", "openapi.json", "permissions.json"} {
		got, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}
		goldenPath := filepath.Join(goldenDir, name)
		if *update {
			if err := os.MkdirAll(goldenDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept)", name)
		}
	}
}

// TestPoolAPICompiles generates fixture-pool-db's Go types and ORM and
// fixture-pool-api's Go types and API module into a temporary tree shaped
// as the dist, and builds and vets the API module: its handler of pick
// calls the ORM's Pick, a method of Account's Pool that takes no key (D104),
// through runTableBehaviorOperation, its other handlers the ORM's Pool and
// Budget operations of a row, its behavior worker's queues its RunDeadline
// and RunEvent, and the event's dispatcher the Workers method with the
// API's event type, read from the queue's message. It then scaffolds the
// implementation beside it and builds that too: its NewWorkers and its stub
// of the event's method. Linking needs the core's archive, which a build
// does not; go vet type-checks the cgo package.
func TestPoolAPICompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	api, db := loadFixturePair(t, poolFixtures)
	apiDir, output := writeFixtureModulesWith(t, poolFixtures, api, db, setPoolDeps)
	implDir := filepath.Join(apiDir, "impl")
	if wrote, err := apigen.WriteImplementationScaffold(output, implDir); err != nil || !wrote {
		t.Fatalf("write the implementation's scaffold: %t, %v", wrote, err)
	}
	for _, args := range [][]string{{"mod", "tidy"}, {"build", "./..."}, {"vet", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	routes, err := os.ReadFile(filepath.Join(apiDir, "routes.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"runTableBehaviorOperation(r.Context(), db.GetAccountRepository().Pool().Pick, input, &result)",
		"runBehaviorOperation(r.Context(), db.GetAccountRepository().Pool().MarkOut, Id, input, &result)",
	} {
		if !strings.Contains(string(routes), want) {
			t.Errorf("routes.go holds no %q", want)
		}
	}
	behaviors, err := os.ReadFile(filepath.Join(apiDir, "behaviors.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"func runTableBehaviorOperation[P, R any](ctx context.Context, op func(context.Context, P) (R, error), input, result any) error {",
		"func AccountRechecksQueues(db *orm.Database, workers Workers, logger *zap.Logger) []worker.Queue {",
		`caller := rules.Caller{Subject: "fixture-pool-api/AccountRechecks", Permissions: []string{"accounts.pool"}}`,
		`case message.UseId == "Pool" && message.Kind == "recheck":`,
		"return workers.AccountRechecksPoolRecheck(ctx, tx, event)",
	} {
		if !strings.Contains(string(behaviors), want) {
			t.Errorf("behaviors.go holds no %q", want)
		}
	}
	scaffold, err := os.ReadFile(filepath.Join(implDir, apigen.ImplementationFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"func NewWorkers(deps api.Deps) (api.Workers, error) {",
		"func (w *workers) AccountRechecksPoolRecheck(ctx context.Context, tx orm.TxInterface, event types.AccountPoolRecheckEvent) error {",
	} {
		if !strings.Contains(string(scaffold), want) {
			t.Errorf("the scaffold holds no %q:\n%s", want, scaffold)
		}
	}
}
