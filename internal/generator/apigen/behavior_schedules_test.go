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

// schedulesFixtures are the fixtures of Schedules (D104): fixture-schedules-api
// serves the operations of fixture-schedules-db's Problem, which implements
// Schedules beside its Workflow on status, from a @behaviorOperations set,
// and declares the behavior worker ProblemWakeUps, which runs Problem's
// deadlines, changes and events, each event handed to the project's Workers
// method ProblemWakeUpsSchedulesRun.
var schedulesFixtures = behaviorFixtures{"fixture-schedules-api", "fixture-schedules-db"}

// setSchedulesDeps sets what the dispatch layer gives an API with workers
// beside its database: the database's Go types module, which declares the
// event queue's message, and the name the package imports it as.
func setSchedulesDeps(output *apigen.APIOutput) {
	output.Deps.DatabaseTypes = "example.com/schemas/types/go/" + schedulesFixtures.db
	output.Deps.DatabaseTypesAlias = "fixtureschedulesdb"
}

// TestWriteAPIGoldenSchedules generates the Go API module of
// fixture-schedules-api and holds the files Problem's Schedules changes to
// their golden copies: the routes, whose handlers run Schedules' and
// Workflow's operations with runBehaviorOperation; behaviors.go, which
// holds it and the queues of ProblemWakeUps, its deadlines, changes and
// events, each event handed to the Workers method of its use and kind by
// problemWakeUpsEvent; deps.go, whose Workers holds that method, the
// worker's one event (D104); the interfaces; the OpenAPI document, which
// leaves the event's types out; and the permission catalog. Regenerate
// with:
// go test ./internal/generator/apigen -run TestWriteAPIGoldenSchedules -update
func TestWriteAPIGoldenSchedules(t *testing.T) {
	api, db := loadFixturePair(t, schedulesFixtures)
	output := generatePairAPI(t, schedulesFixtures, api, db)
	setSchedulesDeps(output)

	var calls []string
	for _, ep := range output.Endpoints {
		if ep.BehaviorOperation() == nil {
			continue
		}
		calls = append(calls, ep.Method+" "+ep.Path+" "+ep.BehaviorRepositoryCall())
	}
	slices.Sort(calls)
	want := []string{
		"POST /api/problem/{id}/schedules/cancel-schedule GetProblemRepository().Schedules().CancelSchedule",
		"POST /api/problem/{id}/schedules/list-schedules GetProblemRepository().Schedules().ListSchedules",
		"POST /api/problem/{id}/schedules/pause-schedule GetProblemRepository().Schedules().PauseSchedule",
		"POST /api/problem/{id}/schedules/report-run GetProblemRepository().Schedules().ReportRun",
		"POST /api/problem/{id}/schedules/resume-schedule GetProblemRepository().Schedules().ResumeSchedule",
		"POST /api/problem/{id}/schedules/set-schedule GetProblemRepository().Schedules().SetSchedule",
		"POST /api/problem/{id}/status/transition GetProblemRepository().Status().Transition",
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("the behavior endpoints = %q, want %q", calls, want)
	}
	if len(output.Workers) != 1 || output.Workers[0].Behavior == nil {
		t.Fatalf("workers = %+v, want the behavior worker ProblemWakeUps", output.Workers)
	}
	w := output.Workers[0]
	if got := []string{w.Behavior.DeadlineQueue, w.Behavior.ChangeQueue, w.Behavior.EventQueue}; !slices.Equal(got, []string{"ProblemDeadline", "ProblemChange", "ProblemEvent"}) || !w.Behavior.Emits() {
		t.Fatalf("ProblemWakeUps' queues = %q, want its deadline, change and event queues", got)
	}
	if got := w.Behavior.Runs(); got != "Problem's deadlines, changes and events" {
		t.Errorf("Runs() = %q", got)
	}
	if !output.HasWorkers() || output.HasOrdinaryWorkers() || !output.HasWorkerEvents() {
		t.Fatalf("HasWorkers %t, HasOrdinaryWorkers %t, HasWorkerEvents %t: Workers holds the event's method alone", output.HasWorkers(), output.HasOrdinaryWorkers(), output.HasWorkerEvents())
	}
	events := output.WorkerEvents()
	if len(events) != 1 || events[0].Method != "ProblemWakeUpsSchedulesRun" || events[0].Event != "ProblemSchedulesRunEvent" || events[0].Queue != "ProblemEvent" {
		t.Fatalf("the worker's events = %+v, want run of Schedules, handed to ProblemWakeUpsSchedulesRun", events)
	}

	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
		RulesGo:         "/repo/runtime/rules/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+schedulesFixtures.api); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	goldenDir := filepath.Join("testdata", "golden", schedulesFixtures.api)
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
	openapi, err := os.ReadFile(filepath.Join(outDir, "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, unwanted := range []string{"ProblemSchedulesRunEvent", "ProblemSchedulesRunPayload"} {
		if strings.Contains(string(openapi), unwanted) {
			t.Errorf("openapi.json names %s, which no endpoint takes or returns", unwanted)
		}
	}
}

// TestSchedulesAPICompiles generates fixture-schedules-db's Go types and
// ORM and fixture-schedules-api's Go types and API module into a temporary
// tree shaped as the dist, and builds and vets the API module: its
// handlers call the ORM's Schedules and Workflow operations with its types
// (D104), its behavior worker's queues its RunDeadline, RunChange and
// RunEvent, and the event's dispatcher the Workers method with the API's
// event type, read from the queue's message. It then scaffolds the
// implementation beside it and builds that too: its NewWorkers and its
// stub of the event's method. Linking needs the core's archive, which a
// build does not; go vet type-checks the cgo package.
func TestSchedulesAPICompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	api, db := loadFixturePair(t, schedulesFixtures)
	apiDir, output := writeFixtureModulesWith(t, schedulesFixtures, api, db, setSchedulesDeps)
	// The scaffold, a package of the API module here, which imports the
	// module's ORM and types as an implementation's module does.
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
	behaviors, err := os.ReadFile(filepath.Join(apiDir, "behaviors.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"func ProblemWakeUpsQueues(db *orm.Database, workers Workers, logger *zap.Logger) []worker.Queue {",
		`caller := rules.Caller{Subject: "fixture-schedules-api/ProblemWakeUps", Permissions: []string{"problems.schedule"}}`,
		"run, err := db.GetProblemRepository().RunEvent(orm.WithBehaviorCaller(ctx, caller), claim.Message, &rules.Claim{ID: claim.ID, Token: claim.Token}, func(ctx context.Context, tx orm.TxInterface) error {",
		"return problemWakeUpsEvent(ctx, workers, tx, claim.Message)",
		`case message.UseId == "Schedules" && message.Kind == "run":`,
		"return workers.ProblemWakeUpsSchedulesRun(ctx, tx, event)",
		`return fmt.Errorf("behavior worker ProblemWakeUps: no Workers method takes event %s of %s on Problem %v (D104)", message.Kind, message.UseId, message.RowKey)`,
	} {
		if !strings.Contains(string(behaviors), want) {
			t.Errorf("behaviors.go holds no %q", want)
		}
	}
	deps, err := os.ReadFile(filepath.Join(apiDir, "deps.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ProblemWakeUpsSchedulesRun(ctx context.Context, tx orm.TxInterface, event types.ProblemSchedulesRunEvent) error",
		"type WorkersConstructor func(deps Deps) (Workers, error)",
	} {
		if !strings.Contains(string(deps), want) {
			t.Errorf("deps.go holds no %q", want)
		}
	}
	scaffold, err := os.ReadFile(filepath.Join(implDir, apigen.ImplementationFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"func NewWorkers(deps api.Deps) (api.Workers, error) {",
		"func (w *workers) ProblemWakeUpsSchedulesRun(ctx context.Context, tx orm.TxInterface, event types.ProblemSchedulesRunEvent) error {",
		`return api.NotImplementedError("event handler ProblemWakeUpsSchedulesRun")`,
	} {
		if !strings.Contains(string(scaffold), want) {
			t.Errorf("the scaffold holds no %q:\n%s", want, scaffold)
		}
	}
}
