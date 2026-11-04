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

// agentsFixtures are the work queue's fixtures on the core (D104):
// fixture-agents-api serves the operations of fixture-agents-db's Job,
// which implements Lease and Retries beside its Workflow on status, of its
// Session, which implements Presence, and of its Account, which implements
// Budget, from @behaviorOperations sets, and declares the behavior workers
// JobDeadlines and SessionPresence.
var agentsFixtures = behaviorFixtures{"fixture-agents-api", "fixture-agents-db"}

// TestWriteAPIGoldenAgents generates the Go API module of
// fixture-agents-api and holds the files its tables' behaviors change to
// their golden copies: the routes, whose handlers of Job's operations run
// them with runBehaviorOperationWithPreconditions, since Job's Lease takes
// a command's preconditions, which each operation's input carries in its
// member preconditions (D104), and whose handlers of Session's and
// Account's run them with runBehaviorOperation; behaviors.go, which holds
// both and the queues of JobDeadlines and SessionPresence, the latter's
// caller holding jobs.override; deps.go and the interfaces, which have no
// method for them; the OpenAPI document; and the permission catalog.
// Regenerate with:
// go test ./internal/generator/apigen -run TestWriteAPIGoldenAgents -update
func TestWriteAPIGoldenAgents(t *testing.T) {
	api, db := loadFixturePair(t, agentsFixtures)
	output := generatePairAPI(t, agentsFixtures, api, db)

	var presenting, plain []string
	for _, ep := range output.Endpoints {
		bo := ep.BehaviorOperation()
		if bo == nil {
			continue
		}
		line := ep.Method + " " + ep.Path + " " + ep.BehaviorRepositoryCall()
		if ep.BehaviorPreconditions {
			if bo.Type != "Job" {
				t.Errorf("%s carries the caller's preconditions: only Job's uses take them", line)
			}
			presenting = append(presenting, line)
		} else {
			plain = append(plain, line)
		}
	}
	want := []string{
		"POST /api/job/{id}/lease/acknowledge GetJobRepository().Lease().Acknowledge",
		"POST /api/job/{id}/lease/acquire GetJobRepository().Lease().Acquire",
		"POST /api/job/{id}/lease/direct GetJobRepository().Lease().Direct",
		"POST /api/job/{id}/lease/expire GetJobRepository().Lease().Expire",
		"POST /api/job/{id}/lease/heartbeat GetJobRepository().Lease().Heartbeat",
		"POST /api/job/{id}/lease/release GetJobRepository().Lease().Release",
		"POST /api/job/{id}/lease/reset-expiries GetJobRepository().Lease().ResetExpiries",
		"POST /api/job/{id}/retries/record-attempt GetJobRepository().Retries().RecordAttempt",
		"POST /api/job/{id}/status/transition GetJobRepository().Status().Transition",
	}
	if !slices.Equal(presenting, want) {
		t.Fatalf("the behavior endpoints that carry preconditions = %q, want %q", presenting, want)
	}
	if len(plain) == 0 {
		t.Fatal("no behavior endpoint of Session or Account")
	}
	if !output.HasBehaviorPreconditions() {
		t.Fatal("HasBehaviorPreconditions is false")
	}
	if output.Deps.Database != agentsFixtures.db || !output.Deps.Rules {
		t.Fatalf("Deps = %+v, want the ORM of %s, which requires the rules module", output.Deps, agentsFixtures.db)
	}
	var workers []string
	for _, w := range output.Workers {
		if w.Behavior == nil {
			t.Fatalf("worker %s is no behavior worker", w.Name)
		}
		workers = append(workers, w.QueuesFunc()+" "+w.Behavior.DeadlineQueue+" "+strings.Join(w.Behavior.Permissions, ","))
	}
	if want := []string{"JobDeadlinesQueues JobDeadline ", "SessionPresenceQueues SessionDeadline jobs.override"}; !slices.Equal(workers, want) {
		t.Fatalf("workers = %q, want %q", workers, want)
	}

	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
		RulesGo:         "/repo/runtime/rules/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+agentsFixtures.api); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	goldenDir := filepath.Join("testdata", "golden", agentsFixtures.api)
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

// TestAgentsAPICompiles generates fixture-agents-db's Go types and ORM and
// fixture-agents-api's Go types and API module into a temporary tree
// shaped as the dist, and builds and vets the API module: its handlers call
// the ORM's Lease, Retries, Presence, Budget and Workflow operations with
// its types, the ones of Job's under orm.WithPreconditions (D104), and its
// behavior workers' queues its RunDeadline. Linking needs the core's
// archive, which a build does not; go vet type-checks the cgo package.
func TestAgentsAPICompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	api, db := loadFixturePair(t, agentsFixtures)
	apiDir := writeFixtureModules(t, agentsFixtures, api, db)
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
		"runBehaviorOperationWithPreconditions(r.Context(), db.GetJobRepository().Lease().Heartbeat, Id, input, &result)",
		"runBehaviorOperationWithPreconditions(r.Context(), db.GetJobRepository().Status().Transition, Id, input, &result)",
		"runBehaviorOperation(r.Context(), db.GetSessionRepository().Presence().Beat, Id, nil, &result)",
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
		"ctx = orm.WithPreconditions(ctx, preconditions)",
		"func JobDeadlinesQueues(db *orm.Database, logger *zap.Logger) []worker.Queue {",
		`caller := rules.Caller{Subject: "fixture-agents-api/SessionPresence", Permissions: []string{"jobs.override"}}`,
	} {
		if !strings.Contains(string(behaviors), want) {
			t.Errorf("behaviors.go holds no %q", want)
		}
	}
}
