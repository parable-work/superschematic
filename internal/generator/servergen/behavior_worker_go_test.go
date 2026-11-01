package servergen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// goBehaviorWorkerGoldenRoot holds the Go entrypoint of fixture-timers-api's
// behavior worker TicketTimers (D103).
const goBehaviorWorkerGoldenRoot = "testdata/golden/behaviors/go-worker"

// goTimersAPI is fixture-timers-api's Go output as the dispatch layer
// hands it to a worker's plan: its Timers operations among its endpoints,
// the behavior worker TicketTimers, which runs fixture-timers-db's Ticket's
// deadline queue, its Deps holding the ORM of fixture-timers-db, whose
// tables implement Timers, and its EnvConfig the database's field.
func goTimersAPI() *apigen.APIOutput {
	n := naming.Default()
	return &apigen.APIOutput{
		SchemaName:  "fixture-timers-api",
		ModulePath:  n.GoAPIModule("fixture-timers-api"),
		TypesModule: n.GoTypesModule("fixture-timers-api"),
		Naming:      n,
		Endpoints: []apigen.EndpointInfo{{
			Name:      "timersFireTimer",
			Path:      "/api/ticket/{id}/timers/fire-timer",
			RoutePath: "/ticket/{id}/timers/fire-timer",
			Method:    "POST",
			Namespace: "ticket",
			Operation: &ir.FieldDef{Name: "timersFireTimer", BehaviorOperation: &ir.BehaviorOperation{
				Database: "fixture-timers-db", Type: "Ticket", Key: "id", Use: "Timers", Behavior: "Timers", Operation: "fireTimer", Writes: true,
			}},
		}},
		Workers: []apigen.WorkerInfo{{
			Name:        "TicketTimers",
			Queue:       "TicketDeadline",
			Concurrency: 2,
			Behavior: &apigen.BehaviorWorkerInfo{
				Table:   "Ticket",
				Queues:  []string{"TicketDeadline"},
				Subject: "fixture-timers-api/TicketTimers",
			},
		}},
		Deps: apigen.DepsInfo{Database: "fixture-timers-db", ORMModule: n.GoORMModule("fixture-timers-db"), Rules: true},
		EnvConfig: &envgen.ConfigOutput{
			SchemaName: "fixture-timers-api",
			EnvConfig:  true,
			Derived:    []envgen.DerivedField{{Key: "FIXTURE_TIMERS_DB_DATABASE", GoName: "FixtureTimersDbDatabase", Kind: ir.EdgeSQL, Service: "fixture-timers-db"}},
		},
	}
}

// TestGoBehaviorWorkerEntrypoint: the Go entrypoint of a behavior worker
// (D103) opens its API's database and ORM, builds no Deps and calls no
// NewWorkers, imports no implementation, and runs the queues the API's
// generated TicketTimersQueues returns with worker.RunAll; its go.mod
// requires and replaces the rules module, which the ORM links, and a
// checkout's image builds the core's archive in a rules stage, as the
// server's does. An ordinary worker's entrypoint is held, byte for byte,
// by TestEntrypointGolden. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestGoBehaviorWorkerEntrypoint -update
func TestGoBehaviorWorkerEntrypoint(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	paths := naming.LocalPaths{
		ScalarGo:      filepath.Join(repoRoot, "third_party", "superscalar", "go"),
		HTTPRuntimeGo: filepath.Join(repoRoot, "runtime", "http", "go"),
		RulesGo:       filepath.Join(repoRoot, "runtime", "rules", "go"),
	}
	n := naming.Default()
	out := filepath.Join(repoRoot, "schemas", "dist")
	o := goTimersAPI()
	in := servergen.Input{
		Stack:  "timers-stack",
		Server: "fixture-timers-api-ticket-timers",
		Dir:    servergen.ServerDir(out, "timers-stack", "fixture-timers-api-ticket-timers"),
		APIs: []servergen.APIInput{{
			Output: o,
			Implementation: servergen.Implementation{
				Dir:       filepath.Join(repoRoot, "services", "fixture-timers-api", "go"),
				Import:    "example.com/services/fixture-timers-api/go",
				Module:    "example.com/services/fixture-timers-api/go",
				ModuleDir: filepath.Join(repoRoot, "services", "fixture-timers-api", "go"),
			},
		}},
		Worker: &servergen.WorkerInput{Name: "TicketTimers", GraceSeconds: 8},
		Modules: []servergen.Module{
			{Path: o.ModulePath, Dir: filepath.Join(out, "api", "fixture-timers-api"), Direct: true},
			{Path: o.TypesModule, Dir: filepath.Join(out, "types", "go", "fixture-timers-api")},
			{Path: n.GoTypesModule("fixture-timers-db"), Dir: filepath.Join(out, "types", "go", "fixture-timers-db")},
			{Path: o.Deps.ORMModule, Dir: filepath.Join(out, "orm", "fixture-timers-db"), Direct: true},
			{Path: n.HTTPRuntimeGoModule, Dir: paths.HTTPRuntimeGo, Direct: true},
			{Path: n.ScalarGoModule, Version: "v1.0.0", Dir: paths.ScalarGo},
			{Path: n.RulesGoModule, Dir: paths.RulesGo},
			{Path: "example.com/services/fixture-timers-api/go", Dir: filepath.Join(repoRoot, "services", "fixture-timers-api", "go"), Direct: true},
		},
		Naming:         n,
		RepositoryRoot: repoRoot,
		ScalarGo:       paths.ScalarGo,
		RulesGo:        paths.RulesGo,
	}
	s, err := servergen.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	if !s.BehaviorWorker() || s.Worker.Method != "" || s.Worker.QueuesFunc() != "TicketTimersQueues" {
		t.Fatalf("the plan's worker = %+v, want the behavior worker TicketTimers", s.Worker)
	}
	if s.Docker == nil {
		t.Fatalf("no Dockerfile: %s", s.NoDocker)
	}
	if err := servergen.Write(s, in.Dir); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, file := range []string{servergen.MainFile, servergen.ModFile, servergen.DockerFile} {
		files[file] = filepath.Join(in.Dir, file)
	}
	compareGoldens(t, goBehaviorWorkerGoldenRoot, files)

	main, err := os.ReadFile(filepath.Join(in.Dir, servergen.MainFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"queues := fixturetimersapi.TicketTimersQueues(fixtureTimersDb, logger)",
		"return worker.RunAll(ctx, queues, worker.Options{Concurrency: concurrency, Grace: 8 * time.Second, Logger: logger})",
		"concurrency, err := worker.Concurrency(2)",
	} {
		if !strings.Contains(string(main), want) {
			t.Errorf("main.go holds no %q:\n%s", want, main)
		}
	}
	for _, unwanted := range []string{"NewWorkers(", "Deps)", "fixturetimersapiimpl"} {
		if strings.Contains(string(main), unwanted) {
			t.Errorf("main.go names %s: a behavior worker builds no Deps and calls no implementation", unwanted)
		}
	}
	docker, err := os.ReadFile(filepath.Join(in.Dir, servergen.DockerFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(docker), "FROM rust:${RUST_VERSION}-trixie AS rules") {
		t.Error("the worker's Dockerfile builds no rules stage, which its ORM links")
	}
}
