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
// deadline queue and change queue, its Deps holding the ORM of
// fixture-timers-db, whose tables implement Timers, and its EnvConfig the
// database's field.
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
				Table:         "Ticket",
				Queues:        []string{"TicketDeadline", "TicketChange"},
				DeadlineQueue: "TicketDeadline",
				ChangeQueue:   "TicketChange",
				Subject:       "fixture-timers-api/TicketTimers",
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

// goAgentsAPI is fixture-agents-api's Go output as the dispatch layer hands
// it to a worker's plan: Job's Lease operations among its endpoints, each
// carrying the caller's preconditions (D104), and the behavior worker
// JobDeadlines, which runs fixture-agents-db's Job's deadline queue, a
// lease's expiry and the end of a backoff, its Deps holding the ORM of
// fixture-agents-db, whose tables implement Lease, Retries, Presence and
// Budget, and its EnvConfig the database's field.
func goAgentsAPI() *apigen.APIOutput {
	n := naming.Default()
	return &apigen.APIOutput{
		SchemaName:  "fixture-agents-api",
		ModulePath:  n.GoAPIModule("fixture-agents-api"),
		TypesModule: n.GoTypesModule("fixture-agents-api"),
		Naming:      n,
		Endpoints: []apigen.EndpointInfo{{
			Name:                  "leaseHeartbeat",
			Path:                  "/api/job/{id}/lease/heartbeat",
			RoutePath:             "/job/{id}/lease/heartbeat",
			Method:                "POST",
			Namespace:             "job",
			BehaviorPreconditions: true,
			Operation: &ir.FieldDef{Name: "leaseHeartbeat", BehaviorOperation: &ir.BehaviorOperation{
				Database: "fixture-agents-db", Type: "Job", Key: "id", Use: "Lease", Behavior: "Lease", Operation: "heartbeat", Writes: true,
			}},
		}},
		Workers: []apigen.WorkerInfo{{
			Name:        "JobDeadlines",
			Queue:       "JobDeadline",
			Concurrency: 2,
			Behavior: &apigen.BehaviorWorkerInfo{
				Table:         "Job",
				Queues:        []string{"JobDeadline"},
				DeadlineQueue: "JobDeadline",
				Subject:       "fixture-agents-api/JobDeadlines",
			},
		}},
		Deps: apigen.DepsInfo{Database: "fixture-agents-db", ORMModule: n.GoORMModule("fixture-agents-db"), Rules: true},
		EnvConfig: &envgen.ConfigOutput{
			SchemaName: "fixture-agents-api",
			EnvConfig:  true,
			Derived:    []envgen.DerivedField{{Key: "FIXTURE_AGENTS_DB_DATABASE", GoName: "FixtureAgentsDbDatabase", Kind: ir.EdgeSQL, Service: "fixture-agents-db"}},
		},
	}
}

// planGoBehaviorWorker plans and writes the Go entrypoint of o's behavior
// worker name in stack, its database db, in a checkout's temporary tree,
// holds its main.go, go.mod and Dockerfile to their golden copies under
// root, and returns its main.go: it opens its API's database and ORM,
// builds no Deps and calls no NewWorkers, imports no implementation, and
// runs the queues the API's generated <Name>Queues returns with
// worker.RunAll; its go.mod requires and replaces the rules module, which
// the ORM links, and a checkout's image builds the core's archive in a
// rules stage, as the server's does.
func planGoBehaviorWorker(t *testing.T, o *apigen.APIOutput, root, stack, name, db string) string {
	t.Helper()
	repoRoot := testpaths.TempDir(t)
	paths := naming.LocalPaths{
		ScalarGo:      filepath.Join(repoRoot, "third_party", "superscalar", "go"),
		HTTPRuntimeGo: filepath.Join(repoRoot, "runtime", "http", "go"),
		RulesGo:       filepath.Join(repoRoot, "runtime", "rules", "go"),
	}
	n := naming.Default()
	out := filepath.Join(repoRoot, "schemas", "dist")
	api := o.SchemaName
	server := api + "-" + kebab(name)
	impl := "example.com/services/" + api + "/go"
	in := servergen.Input{
		Stack:  stack,
		Server: server,
		Dir:    servergen.ServerDir(out, stack, server),
		APIs: []servergen.APIInput{{
			Output: o,
			Implementation: servergen.Implementation{
				Dir:       filepath.Join(repoRoot, "services", api, "go"),
				Import:    impl,
				Module:    impl,
				ModuleDir: filepath.Join(repoRoot, "services", api, "go"),
			},
		}},
		Worker: &servergen.WorkerInput{Name: name, GraceSeconds: 8},
		Modules: []servergen.Module{
			{Path: o.ModulePath, Dir: filepath.Join(out, "api", api), Direct: true},
			{Path: o.TypesModule, Dir: filepath.Join(out, "types", "go", api)},
			{Path: n.GoTypesModule(db), Dir: filepath.Join(out, "types", "go", db)},
			{Path: o.Deps.ORMModule, Dir: filepath.Join(out, "orm", db), Direct: true},
			{Path: n.HTTPRuntimeGoModule, Dir: paths.HTTPRuntimeGo, Direct: true},
			{Path: n.ScalarGoModule, Version: "v1.0.0", Dir: paths.ScalarGo},
			{Path: n.RulesGoModule, Dir: paths.RulesGo},
			{Path: impl, Dir: filepath.Join(repoRoot, "services", api, "go"), Direct: true},
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
	if !s.BehaviorWorker() || s.Worker.Method != "" || s.Worker.QueuesFunc() != name+"Queues" {
		t.Fatalf("the plan's worker = %+v, want the behavior worker %s", s.Worker, name)
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
	compareGoldens(t, root, files)

	main, err := os.ReadFile(filepath.Join(in.Dir, servergen.MainFile))
	if err != nil {
		t.Fatal(err)
	}
	implPackage := strings.ReplaceAll(api, "-", "") + "impl"
	if s.EventWorker() {
		// A behavior worker whose table's behaviors emit events builds the
		// API's Deps and the implementation's Workers, whose methods its
		// events go to (D104).
		for _, wanted := range []string{implPackage + ".NewWorkers(", "Deps{", implPackage + " \"" + impl + "\""} {
			if !strings.Contains(string(main), wanted) {
				t.Errorf("main.go names no %s: a behavior worker whose table emits events hands them to the implementation's Workers", wanted)
			}
		}
	} else {
		for _, unwanted := range []string{"NewWorkers(", "Deps)", implPackage} {
			if strings.Contains(string(main), unwanted) {
				t.Errorf("main.go names %s: a behavior worker builds no Deps and calls no implementation", unwanted)
			}
		}
	}
	docker, err := os.ReadFile(filepath.Join(in.Dir, servergen.DockerFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(docker), "FROM rust:${RUST_VERSION}-trixie AS rules") {
		t.Error("the worker's Dockerfile builds no rules stage, which its ORM links")
	}
	return string(main)
}

// kebab is a worker's class name as its server's name ends in
// (TicketTimers, ticket-timers).
func kebab(name string) string {
	var out strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			out.WriteByte('-')
		}
		out.WriteRune(r)
	}
	return strings.ToLower(out.String())
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
	main := planGoBehaviorWorker(t, goTimersAPI(), goBehaviorWorkerGoldenRoot, "timers-stack", "TicketTimers", "fixture-timers-db")
	for _, want := range []string{
		"queues := fixturetimersapi.TicketTimersQueues(fixtureTimersDb, logger)",
		"return worker.RunAll(ctx, queues, worker.Options{Concurrency: concurrency, Grace: 8 * time.Second, Logger: logger})",
		"concurrency, err := worker.Concurrency(2)",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("main.go holds no %q:\n%s", want, main)
		}
	}
}

// goAgentsWorkerGoldenRoot holds the Go entrypoint of fixture-agents-api's
// behavior worker JobDeadlines (D104).
const goAgentsWorkerGoldenRoot = "testdata/golden/behaviors/go-worker-agents"

// TestGoBehaviorWorkerEntrypointOfJobDeadlines: the Go entrypoint of
// JobDeadlines, which runs Job's deadlines (D104), a lease's expiry and the
// end of a backoff, as TicketTimers' runs Ticket's: its queues are
// JobDeadlinesQueues', whose handlers run through the ORM's RunDeadline,
// and its API serves operations whose input carries the caller's
// preconditions, which the entrypoint does not read. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestGoBehaviorWorkerEntrypointOfJobDeadlines -update
func TestGoBehaviorWorkerEntrypointOfJobDeadlines(t *testing.T) {
	main := planGoBehaviorWorker(t, goAgentsAPI(), goAgentsWorkerGoldenRoot, "agents-stack", "JobDeadlines", "fixture-agents-db")
	for _, want := range []string{
		"queues := fixtureagentsapi.JobDeadlinesQueues(fixtureAgentsDb, logger)",
		"return worker.RunAll(ctx, queues, worker.Options{Concurrency: concurrency, Grace: 8 * time.Second, Logger: logger})",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("main.go holds no %q:\n%s", want, main)
		}
	}
}
