package apigen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
)

// TestWriteAPIGoldenTimers generates the Go API module of
// fixture-timers-api, which serves the operations of fixture-timers-db's
// Ticket, Timers' among them under its member, and declares the behavior
// worker TicketTimers (D103), and holds the files they change to their
// golden copies: the routes, whose handlers call the ORM's
// Timers().FireTimer and the rest; deps.go and the interfaces, which have
// no method for the worker; and behaviors.go, which holds the worker's
// queues, its deadline queue's messages run through the ORM's RunDeadline
// and its change queue's through RunChange, each as the worker.
// Regenerate with:
// go test ./internal/generator/apigen -run TestWriteAPIGoldenTimers -update
func TestWriteAPIGoldenTimers(t *testing.T) {
	api, db := loadFixturePair(t, timersFixtures)
	output := generatePairAPI(t, timersFixtures, api, db)

	var behaviors []string
	for _, ep := range output.Endpoints {
		if bo := ep.BehaviorOperation(); bo != nil {
			behaviors = append(behaviors, ep.Method+" "+ep.Path+" "+ep.BehaviorRepositoryCall())
		}
	}
	want := []string{
		"POST /api/ticket/{id}/status/transition GetTicketRepository().Status().Transition",
		"POST /api/ticket/{id}/timers/fire-timer GetTicketRepository().Timers().FireTimer",
		"POST /api/ticket/{id}/timers/reset-timer GetTicketRepository().Timers().ResetTimer",
		"POST /api/ticket/{id}/timers/snooze-timer GetTicketRepository().Timers().SnoozeTimer",
	}
	if !slices.Equal(behaviors, want) {
		t.Fatalf("behavior endpoints = %q, want %q", behaviors, want)
	}
	wantWorkers := []apigen.WorkerInfo{{
		Name:        "TicketTimers",
		Queue:       "TicketDeadline",
		Concurrency: 2,
		Behavior: &apigen.BehaviorWorkerInfo{
			Table:         "Ticket",
			Queues:        []string{"TicketDeadline", "TicketChange"},
			DeadlineQueue: "TicketDeadline",
			ChangeQueue:   "TicketChange",
			Subject:       "fixture-timers-api/TicketTimers",
			Behaviors:     []string{"Timers"},
			Hearing:       []string{"Timers"},
		},
	}}
	if !reflect.DeepEqual(output.Workers, wantWorkers) {
		t.Fatalf("workers = %+v, want %+v", output.Workers, wantWorkers)
	}
	if output.HasWorkers() || !output.HasBehaviorWorkers() || len(output.OrdinaryWorkers()) != 0 {
		t.Fatalf("HasWorkers %t, HasBehaviorWorkers %t: a behavior worker is no method of Workers", output.HasWorkers(), output.HasBehaviorWorkers())
	}
	if got := output.Workers[0].QueuesFunc(); got != "TicketTimersQueues" {
		t.Fatalf("QueuesFunc = %q, want TicketTimersQueues", got)
	}
	if output.Deps.Database != timersFixtures.db || !output.Deps.Rules {
		t.Fatalf("Deps = %+v, want the ORM of %s, which requires the rules module", output.Deps, timersFixtures.db)
	}

	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
		RulesGo:         "/repo/runtime/rules/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+timersFixtures.api); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	goldenDir := filepath.Join("testdata", "golden", timersFixtures.api)
	for _, name := range []string{"go.mod", "routes.go", "interfaces.go", "behaviors.go", "deps.go"} {
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
	deps, err := os.ReadFile(filepath.Join(outDir, "deps.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(deps), "type Workers interface") {
		t.Error("deps.go declares Workers: a behavior worker's handler is generated, and the API has no other worker")
	}
}

// TestTimersAPICompiles generates fixture-timers-db's Go types and ORM and
// fixture-timers-api's Go types and API module into a temporary tree
// shaped as the dist, and builds and vets the API module: its behavior
// operations' handlers and its behavior worker's queues call the ORM with
// its types (D103). Linking needs the core's archive, which a build does
// not; go vet type-checks the cgo package.
func TestTimersAPICompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	api, db := loadFixturePair(t, timersFixtures)
	apiDir := writeFixtureModules(t, timersFixtures, api, db)
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
		"func TicketTimersQueues(db *orm.Database, logger *zap.Logger) []worker.Queue {",
		`caller := rules.Caller{Subject: "fixture-timers-api/TicketTimers"}`,
		"db.GetTicketRepository().RunDeadline(orm.WithBehaviorCaller(ctx, caller), claim.Message, &rules.Claim{ID: claim.ID, Token: claim.Token})",
		"db.GetTicketRepository().RunChange(orm.WithBehaviorCaller(ctx, caller), claim.Message, &rules.Claim{ID: claim.ID, Token: claim.Token})",
		"Complete: func(context.Context, string, string) error { return nil },",
	} {
		if !strings.Contains(string(behaviors), want) {
			t.Errorf("behaviors.go holds no %q", want)
		}
	}
	// The deadline queue comes first, so a caller's [0] is still it.
	if d, c := strings.Index(string(behaviors), `Name:  "TicketDeadline"`), strings.Index(string(behaviors), `Name:  "TicketChange"`); d < 0 || c < 0 || d > c {
		t.Errorf("behaviors.go's queues are not the deadline queue, then the change queue")
	}
}

// TestReactionsAPICompiles generates fixture-reactions-db's Go types and
// ORM and fixture-reactions-api's Go types and API module, and builds and
// vets the API module: two behavior workers, each of a table whose
// behaviors arm no deadlines and whose change queue alone it runs through
// the ORM's RunChange (D103).
func TestReactionsAPICompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	api, db := loadFixturePair(t, reactionsFixtures)
	output := generatePairAPI(t, reactionsFixtures, api, db)
	var workers []string
	for _, w := range output.BehaviorWorkers() {
		workers = append(workers, w.Name+" "+w.Behavior.Runs()+" "+strings.Join(w.Behavior.Queues, ","))
	}
	if want := []string{"ProjectReactions Project's changes ProjectChange", "TaskReactions Task's changes TaskChange"}; !slices.Equal(workers, want) {
		t.Fatalf("behavior workers = %q, want %q", workers, want)
	}
	apiDir := writeFixtureModules(t, reactionsFixtures, api, db)
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
		"func ProjectReactionsQueues(db *orm.Database, logger *zap.Logger) []worker.Queue {",
		"func TaskReactionsQueues(db *orm.Database, logger *zap.Logger) []worker.Queue {",
		`caller := rules.Caller{Subject: "fixture-reactions-api/TaskReactions"}`,
		"db.GetTaskRepository().RunChange(orm.WithBehaviorCaller(ctx, caller), claim.Message, &rules.Claim{ID: claim.ID, Token: claim.Token})",
		`logger.Info("change ran nothing", zap.String("queue", "TaskChange")`,
	} {
		if !strings.Contains(string(behaviors), want) {
			t.Errorf("behaviors.go holds no %q", want)
		}
	}
	if strings.Contains(string(behaviors), "RunDeadline") {
		t.Error("behaviors.go runs a deadline, and neither table arms one")
	}
}
