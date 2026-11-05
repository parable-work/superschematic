package servergen_test

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// goSeatsAPI is fixture-seats-api's Go output as the dispatch layer hands
// it to a worker's plan: Sandbox's observe among its endpoints, an
// operation of a Reconcile on a field (D104), and the behavior worker
// SandboxDeadlines, which runs fixture-seats-db's Sandbox's deadline queue,
// its stale deadline, its Deps holding the ORM of fixture-seats-db, whose
// tables compose Seats and Reconcile, which only the behaviors' core
// implements, and its EnvConfig the database's field.
func goSeatsAPI() *apigen.APIOutput {
	n := naming.Default()
	return &apigen.APIOutput{
		SchemaName:  "fixture-seats-api",
		ModulePath:  n.GoAPIModule("fixture-seats-api"),
		TypesModule: n.GoTypesModule("fixture-seats-api"),
		Naming:      n,
		Endpoints: []apigen.EndpointInfo{{
			Name:      "observedObserve",
			Path:      "/api/sandbox/{id}/observed/observe",
			RoutePath: "/sandbox/{id}/observed/observe",
			Method:    "POST",
			Namespace: "sandbox",
			Operation: &ir.FieldDef{Name: "observedObserve", BehaviorOperation: &ir.BehaviorOperation{
				Database: "fixture-seats-db", Type: "Sandbox", Key: "id", Use: "observed", Behavior: "Reconcile", Operation: "observe", Writes: true,
			}},
		}},
		Workers: []apigen.WorkerInfo{{
			Name:        "SandboxDeadlines",
			Queue:       "SandboxDeadline",
			Concurrency: 2,
			Behavior: &apigen.BehaviorWorkerInfo{
				Table:         "Sandbox",
				Queues:        []string{"SandboxDeadline"},
				DeadlineQueue: "SandboxDeadline",
				Subject:       "fixture-seats-api/SandboxDeadlines",
			},
		}},
		Deps: apigen.DepsInfo{Database: "fixture-seats-db", ORMModule: n.GoORMModule("fixture-seats-db"), Rules: true},
		EnvConfig: &envgen.ConfigOutput{
			SchemaName: "fixture-seats-api",
			EnvConfig:  true,
			Derived:    []envgen.DerivedField{{Key: "FIXTURE_SEATS_DB_DATABASE", GoName: "FixtureSeatsDbDatabase", Kind: ir.EdgeSQL, Service: "fixture-seats-db"}},
		},
	}
}

// goSeatsWorkerGoldenRoot holds the Go entrypoint of fixture-seats-api's
// behavior worker SandboxDeadlines (D104).
const goSeatsWorkerGoldenRoot = "testdata/golden/behaviors/go-worker-seats"

// TestGoBehaviorWorkerEntrypointOfSandboxDeadlines: the Go entrypoint of
// SandboxDeadlines, which runs Sandbox's stale deadline (D104), the move of
// a sandbox no observation has reached to unreachable, as JobDeadlines'
// runs Job's: its queues are SandboxDeadlinesQueues', whose handlers run
// through the ORM's RunDeadline, so a behavior only the core implements, on
// a field of a table, plans as any other. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestGoBehaviorWorkerEntrypointOfSandboxDeadlines -update
func TestGoBehaviorWorkerEntrypointOfSandboxDeadlines(t *testing.T) {
	main := planGoBehaviorWorker(t, goSeatsAPI(), goSeatsWorkerGoldenRoot, "seats-stack", "SandboxDeadlines", "fixture-seats-db")
	for _, want := range []string{
		"queues := fixtureseatsapi.SandboxDeadlinesQueues(fixtureSeatsDb, logger)",
		"return worker.RunAll(ctx, queues, worker.Options{Concurrency: concurrency, Grace: 8 * time.Second, Logger: logger})",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("main.go holds no %q:\n%s", want, main)
		}
	}
}
