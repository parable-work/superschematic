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

// seatsFixtures are the fixtures of the behaviors only the core implements
// (D104): fixture-seats-api serves the operations of fixture-seats-db's
// Problem, which implements Seats and StateRules beside its Workflow on
// status, whose by names seats, and of its Sandbox, whose observed is a
// Reconcile beside its Workflow on status, from @behaviorOperations sets,
// and declares the behavior worker SandboxDeadlines.
var seatsFixtures = behaviorFixtures{"fixture-seats-api", "fixture-seats-db"}

// TestWriteAPIGoldenSeats generates the Go API module of fixture-seats-api
// and holds the files its tables' behaviors change to their golden copies:
// the routes, whose handlers of Problem's Seats and Workflow operations and
// of Sandbox's Reconcile and Workflow operations run them with
// runBehaviorOperation, since no use of either table takes a command's
// preconditions; behaviors.go, which holds it and the queues of
// SandboxDeadlines, whose caller holds no permission; deps.go and the
// interfaces, which have no method for them; the OpenAPI document; and the
// permission catalog. Regenerate with:
// go test ./internal/generator/apigen -run TestWriteAPIGoldenSeats -update
func TestWriteAPIGoldenSeats(t *testing.T) {
	api, db := loadFixturePair(t, seatsFixtures)
	output := generatePairAPI(t, seatsFixtures, api, db)

	var calls []string
	for _, ep := range output.Endpoints {
		if ep.BehaviorOperation() == nil {
			continue
		}
		line := ep.Method + " " + ep.Path + " " + ep.BehaviorRepositoryCall()
		if ep.BehaviorPreconditions {
			t.Errorf("%s carries the caller's preconditions: no use of fixture-seats-db takes them", line)
		}
		calls = append(calls, line)
	}
	slices.Sort(calls)
	want := []string{
		"POST /api/problem/{id}/seats/leave-seat GetProblemRepository().Seats().LeaveSeat",
		"POST /api/problem/{id}/seats/list-seats GetProblemRepository().Seats().ListSeats",
		"POST /api/problem/{id}/seats/take-seat GetProblemRepository().Seats().TakeSeat",
		"POST /api/problem/{id}/status/transition GetProblemRepository().Status().Transition",
		"POST /api/sandbox/{id}/observed/observe GetSandboxRepository().Observed().Observe",
		"POST /api/sandbox/{id}/status/transition GetSandboxRepository().Status().Transition",
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("the behavior endpoints = %q, want %q", calls, want)
	}
	if output.HasBehaviorPreconditions() {
		t.Fatal("HasBehaviorPreconditions is true, and no use takes a command's preconditions")
	}
	if output.Deps.Database != seatsFixtures.db || !output.Deps.Rules {
		t.Fatalf("Deps = %+v, want the ORM of %s, which requires the rules module", output.Deps, seatsFixtures.db)
	}
	var workers []string
	for _, w := range output.Workers {
		if w.Behavior == nil {
			t.Fatalf("worker %s is no behavior worker", w.Name)
		}
		workers = append(workers, w.QueuesFunc()+" "+w.Behavior.DeadlineQueue+" "+w.Behavior.ChangeQueue+" "+strings.Join(w.Behavior.Permissions, ","))
	}
	if want := []string{"SandboxDeadlinesQueues SandboxDeadline  "}; !slices.Equal(workers, want) {
		t.Fatalf("workers = %q, want %q", workers, want)
	}

	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
		RulesGo:         "/repo/runtime/rules/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+seatsFixtures.api); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	goldenDir := filepath.Join("testdata", "golden", seatsFixtures.api)
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

// TestSeatsAPICompiles generates fixture-seats-db's Go types and ORM and
// fixture-seats-api's Go types and API module into a temporary tree shaped
// as the dist, and builds and vets the API module: its handlers call the
// ORM's Seats, Reconcile and Workflow operations with its types (D104), and
// its behavior worker's queues its RunDeadline. Linking needs the core's
// archive, which a build does not; go vet type-checks the cgo package.
func TestSeatsAPICompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	api, db := loadFixturePair(t, seatsFixtures)
	apiDir := writeFixtureModules(t, seatsFixtures, api, db)
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
		"runBehaviorOperation(r.Context(), db.GetProblemRepository().Seats().TakeSeat, Id, input, &result)",
		"runBehaviorOperation(r.Context(), db.GetProblemRepository().Seats().ListSeats, Id, input, &result)",
		"runBehaviorOperation(r.Context(), db.GetSandboxRepository().Observed().Observe, Id, input, &result)",
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
		"func SandboxDeadlinesQueues(db *orm.Database, logger *zap.Logger) []worker.Queue {",
		`caller := rules.Caller{Subject: "fixture-seats-api/SandboxDeadlines"}`,
	} {
		if !strings.Contains(string(behaviors), want) {
			t.Errorf("behaviors.go holds no %q", want)
		}
	}
	if strings.Contains(string(behaviors), "runBehaviorOperationWithPreconditions") {
		t.Error("behaviors.go holds runBehaviorOperationWithPreconditions, and no use takes a command's preconditions")
	}
}
