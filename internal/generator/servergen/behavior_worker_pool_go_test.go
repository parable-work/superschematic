package servergen_test

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// goPoolAPI is fixture-pool-api's Go output as the dispatch layer hands it
// to a worker's plan: Account's pick, an operation of the table as a whole,
// among its endpoints, and the behavior worker AccountRechecks, which runs
// fixture-pool-db's Account's deadline and event queues (D104), each recheck
// event handed to the Workers method AccountRechecksPoolRecheck, its Deps
// holding the ORM of fixture-pool-db, and its EnvConfig the database's
// field.
func goPoolAPI() *apigen.APIOutput {
	n := naming.Default()
	return &apigen.APIOutput{
		SchemaName:  "fixture-pool-api",
		ModulePath:  n.GoAPIModule("fixture-pool-api"),
		TypesModule: n.GoTypesModule("fixture-pool-api"),
		Naming:      n,
		Endpoints: []apigen.EndpointInfo{{
			Name:      "poolPick",
			Path:      "/api/account/pool/pick",
			RoutePath: "/account/pool/pick",
			Method:    "POST",
			Namespace: "account",
			Operation: &ir.FieldDef{Name: "poolPick", BehaviorOperation: &ir.BehaviorOperation{
				Database: "fixture-pool-db", Type: "Account", Key: "id", Use: "Pool", Behavior: "Pool", Operation: "pick", Table: true,
			}},
		}},
		Workers: []apigen.WorkerInfo{{
			Name:        "AccountRechecks",
			Queue:       "AccountDeadline",
			Concurrency: 2,
			Behavior: &apigen.BehaviorWorkerInfo{
				Table:         "Account",
				Queues:        []string{"AccountDeadline", "AccountEvent"},
				DeadlineQueue: "AccountDeadline",
				EventQueue:    "AccountEvent",
				Events: []ir.BehaviorWorkerEvent{{
					Use: "Pool", Member: "pool", Behavior: "Pool", Kind: "recheck",
					Method: "AccountRechecksPoolRecheck", Event: "AccountPoolRecheckEvent", Payload: "AccountPoolRecheckPayload",
				}},
				Subject:     "fixture-pool-api/AccountRechecks",
				Permissions: []string{"accounts.pool"},
			},
		}},
		Deps: apigen.DepsInfo{
			Database: "fixture-pool-db", ORMModule: n.GoORMModule("fixture-pool-db"), Rules: true,
			DatabaseTypes: n.GoTypesModule("fixture-pool-db"), DatabaseTypesAlias: "fixturepooldb",
		},
		EnvConfig: &envgen.ConfigOutput{
			SchemaName: "fixture-pool-api",
			EnvConfig:  true,
			Derived:    []envgen.DerivedField{{Key: "FIXTURE_POOL_DB_DATABASE", GoName: "FixturePoolDbDatabase", Kind: ir.EdgeSQL, Service: "fixture-pool-db"}},
		},
	}
}

// goPoolWorkerGoldenRoot holds the Go entrypoint of fixture-pool-api's
// behavior worker AccountRechecks (D104).
const goPoolWorkerGoldenRoot = "testdata/golden/behaviors/go-worker-pool"

// TestGoBehaviorWorkerEntrypointOfAccountRechecks: the Go entrypoint of
// AccountRechecks, whose table's Pool arms a member's recheck and emits the
// event recheck when it falls due (D104): it builds the API's Deps and the
// implementation's Workers, as a @worker's entrypoint does, and runs
// AccountRechecksQueues' two queues, deadlines and events, with
// worker.RunAll, each event handed to the Workers method of its use and
// kind; its go.mod requires the implementation. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestGoBehaviorWorkerEntrypointOfAccountRechecks -update
func TestGoBehaviorWorkerEntrypointOfAccountRechecks(t *testing.T) {
	main := planGoBehaviorWorker(t, goPoolAPI(), goPoolWorkerGoldenRoot, "pool-stack", "AccountRechecks", "fixture-pool-db")
	for _, want := range []string{
		"workers, err := fixturepoolapiimpl.NewWorkers(fixturePoolApiDeps)",
		"queues := fixturepoolapi.AccountRechecksQueues(fixturePoolDb, workers, logger)",
		"return worker.RunAll(ctx, queues, worker.Options{Concurrency: concurrency, Grace: 8 * time.Second, Logger: logger})",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("main.go holds no %q:\n%s", want, main)
		}
	}
}
