package servergen_test

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// goSchedulesAPI is fixture-schedules-api's Go output as the dispatch layer
// hands it to a worker's plan: Problem's setSchedule among its endpoints,
// and the behavior worker ProblemWakeUps, which runs fixture-schedules-db's
// Problem's deadline, change and event queues (D104), each event handed to
// the Workers method ProblemWakeUpsSchedulesRun, its Deps holding the ORM of
// fixture-schedules-db, and its EnvConfig the database's field.
func goSchedulesAPI() *apigen.APIOutput {
	n := naming.Default()
	return &apigen.APIOutput{
		SchemaName:  "fixture-schedules-api",
		ModulePath:  n.GoAPIModule("fixture-schedules-api"),
		TypesModule: n.GoTypesModule("fixture-schedules-api"),
		Naming:      n,
		Endpoints: []apigen.EndpointInfo{{
			Name:      "schedulesSetSchedule",
			Path:      "/api/problem/{id}/schedules/set-schedule",
			RoutePath: "/problem/{id}/schedules/set-schedule",
			Method:    "POST",
			Namespace: "problem",
			Operation: &ir.FieldDef{Name: "schedulesSetSchedule", BehaviorOperation: &ir.BehaviorOperation{
				Database: "fixture-schedules-db", Type: "Problem", Key: "id", Use: "Schedules", Behavior: "Schedules", Operation: "setSchedule", Writes: true,
			}},
		}},
		Workers: []apigen.WorkerInfo{{
			Name:        "ProblemWakeUps",
			Queue:       "ProblemDeadline",
			Concurrency: 2,
			Behavior: &apigen.BehaviorWorkerInfo{
				Table:         "Problem",
				Queues:        []string{"ProblemDeadline", "ProblemChange", "ProblemEvent"},
				DeadlineQueue: "ProblemDeadline",
				ChangeQueue:   "ProblemChange",
				EventQueue:    "ProblemEvent",
				Events: []ir.BehaviorWorkerEvent{{
					Use: "Schedules", Member: "schedules", Behavior: "Schedules", Kind: "run",
					Method: "ProblemWakeUpsSchedulesRun", Event: "ProblemSchedulesRunEvent", Payload: "ProblemSchedulesRunPayload",
				}},
				Subject:     "fixture-schedules-api/ProblemWakeUps",
				Permissions: []string{"problems.schedule"},
			},
		}},
		Deps: apigen.DepsInfo{
			Database: "fixture-schedules-db", ORMModule: n.GoORMModule("fixture-schedules-db"), Rules: true,
			DatabaseTypes: n.GoTypesModule("fixture-schedules-db"), DatabaseTypesAlias: "fixtureschedulesdb",
		},
		EnvConfig: &envgen.ConfigOutput{
			SchemaName: "fixture-schedules-api",
			EnvConfig:  true,
			Derived:    []envgen.DerivedField{{Key: "FIXTURE_SCHEDULES_DB_DATABASE", GoName: "FixtureSchedulesDbDatabase", Kind: ir.EdgeSQL, Service: "fixture-schedules-db"}},
		},
	}
}

// goSchedulesWorkerGoldenRoot holds the Go entrypoint of
// fixture-schedules-api's behavior worker ProblemWakeUps (D104).
const goSchedulesWorkerGoldenRoot = "testdata/golden/behaviors/go-worker-schedules"

// TestGoBehaviorWorkerEntrypointOfProblemWakeUps: the Go entrypoint of
// ProblemWakeUps, whose table's Schedules emits the event run (D104): it
// builds the API's Deps and the implementation's Workers, as a @worker's
// entrypoint does, and runs ProblemWakeUpsQueues' three queues, deadlines,
// changes and events, with worker.RunAll, each event handed to the Workers
// method of its use and kind; its go.mod requires the implementation.
// Regenerate with:
//
//	go test ./internal/generator/servergen -run TestGoBehaviorWorkerEntrypointOfProblemWakeUps -update
func TestGoBehaviorWorkerEntrypointOfProblemWakeUps(t *testing.T) {
	main := planGoBehaviorWorker(t, goSchedulesAPI(), goSchedulesWorkerGoldenRoot, "schedules-stack", "ProblemWakeUps", "fixture-schedules-db")
	for _, want := range []string{
		"workers, err := fixtureschedulesapiimpl.NewWorkers(fixtureSchedulesApiDeps)",
		"queues := fixtureschedulesapi.ProblemWakeUpsQueues(fixtureSchedulesDb, workers, logger)",
		"return worker.RunAll(ctx, queues, worker.Options{Concurrency: concurrency, Grace: 8 * time.Second, Logger: logger})",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("main.go holds no %q:\n%s", want, main)
		}
	}
}
