package tsrestgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sdkgen"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// timersAPI is the API that serves fixture-timers-db's Ticket (D103): its
// status's and its Timers' operations under TicketActions, which needs
// tickets.write, and the behavior worker TicketTimers, which runs its
// deadlines.
const timersAPI = "fixture-timers-api"

// timersDB is fixture-timers-api's database, its one DB dependency.
const timersDB = "fixture-timers-db"

// timersFixture is fixture-timers-api with its database and apigen's
// endpoints for it, read with one registry, which declares the
// behaviors; the router serves its MCP tools.
func timersFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, timersDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", timersDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, timersAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", timersAPI, err)
	}
	deps := map[string]*ir.Schema{timersDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     timersAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: timersAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, mcp: true, depsInfo: timersDeps}
}

// timersDeps is fixture-timers-api's Deps as the dispatch layer sets it: a
// pool of its database, which lists postgres first, and the repositories
// its TypeScript types export.
var timersDeps = DepsInfo{
	Database:             timersDB,
	Dialect:              "postgres",
	ORM:                  "FixtureTimersDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-timers-db-types",
}

// generateTimersFixture generates the package for fixture-timers-api, its
// Deps set.
func generateTimersFixture(t *testing.T) *APIOutput {
	t.Helper()
	output := generateBehavior(t, timersFixture(t))
	if err := output.SetDeps(timersDeps); err != nil {
		t.Fatalf("SetDeps: %v", err)
	}
	return output
}

// TestWriteAPIGoldenBehaviorTimers pins the package for
// fixture-timers-api: its router mounts Timers' operations under the
// behavior's member, and behavior-workers.ts exports TicketTimers' queues,
// which index.ts exports; the implementation has no worker. Regenerate
// with go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenBehaviorTimers -update
func TestWriteAPIGoldenBehaviorTimers(t *testing.T) {
	output := generateTimersFixture(t)
	checkGolden(t, output, timersAPI)
	// behavior-workers.ts, which only an API with a behavior worker has.
	dir := t.TempDir()
	if err := WriteAPI(output, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, BehaviorWorkersFile))
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "golden", timersAPI, BehaviorWorkersFile)
	if *update {
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if want, err := os.ReadFile(golden); err != nil || string(got) != string(want) {
		t.Errorf("%s differs from golden (run with -update to accept): %v", BehaviorWorkersFile, err)
	}
}

// TestGenerateBehaviorWorker: a behavior worker (D103) is the generator's,
// not the implementation's. It has no method of Workers and no queue
// deps.ts imports; its handler is behavior-workers.ts's, which builds its
// queues over the database's repositories as the worker's subject. A
// type-anchored use's operations call the repository under the behavior's
// member, ticket.timers.fireTimer.
func TestGenerateBehaviorWorker(t *testing.T) {
	output := generateTimersFixture(t)
	if !output.TableBehaviors || !output.HasBehaviors() || !output.HasBehaviorWorkers() || output.HasWorkers() {
		t.Errorf("TableBehaviors %t, HasBehaviors %t, HasBehaviorWorkers %t, HasWorkers %t", output.TableBehaviors, output.HasBehaviors(), output.HasBehaviorWorkers(), output.HasWorkers())
	}
	workers := output.BehaviorWorkers()
	if len(workers) != 1 {
		t.Fatalf("behavior workers = %+v", workers)
	}
	w := workers[0]
	want := apigen.BehaviorWorkerInfo{Table: "Ticket", Queues: []string{"TicketDeadline"}, Subject: "fixture-timers-api/TicketTimers"}
	if w.Name != "TicketTimers" || w.Method != "" || w.Queue != "TicketDeadline" || w.Concurrency != 2 || w.Behavior.Table != want.Table || !slices.Equal(w.Behavior.Queues, want.Queues) || w.Behavior.Subject != want.Subject || len(w.Behavior.Permissions) != 0 {
		t.Errorf("TicketTimers = %+v, behavior %+v", w, *w.Behavior)
	}
	if w.QueuesFunc() != "ticketTimersQueues" || w.CallerConst() != "ticketTimersCaller" || w.Repository() != "ticket" || w.DeadlineQueueConst() != "ticketDeadlineQueue" {
		t.Errorf("TicketTimers' names: %s, %s, %s, %s", w.QueuesFunc(), w.CallerConst(), w.Repository(), w.DeadlineQueueConst())
	}
	if queues := output.WorkerQueues(); len(queues) != 0 {
		t.Errorf("deps.ts imports the queues %v, which no @worker handles", queues)
	}
	routes := map[string]string{}
	for _, route := range output.BehaviorRoutes() {
		routes[route.Use+"."+route.Operation] = route.Method()
	}
	for use, method := range map[string]string{
		"Timers.fireTimer":   "ticket.timers.fireTimer",
		"Timers.resetTimer":  "ticket.timers.resetTimer",
		"Timers.snoozeTimer": "ticket.timers.snoozeTimer",
		"status.transition":  "ticket.status.transition",
	} {
		if routes[use] != method {
			t.Errorf("%s calls %q, want %q (routes %v)", use, routes[use], method, routes)
		}
	}

	dir := t.TempDir()
	if err := WriteAPI(output, dir); err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(dir, "index.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "export * from './behavior-workers';") {
		t.Errorf("index.ts exports no behavior-workers:\n%s", index)
	}
	deps, err := os.ReadFile(filepath.Join(dir, "deps.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(deps), "interface Workers") {
		t.Errorf("deps.ts declares Workers for a behavior worker:\n%s", deps)
	}
	// A rewrite without the worker removes the file.
	output.Workers = nil
	if err := WriteAPI(output, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, BehaviorWorkersFile)); !os.IsNotExist(err) {
		t.Errorf("%s stays after its worker went: %v", BehaviorWorkersFile, err)
	}
}

// TestBehaviorWorkerNeedsTheRepositories: a behavior worker runs its
// table's deadlines through Deps.orm, on Postgres, so Deps over a
// database that lists sqlite first, or without the repositories, is
// refused, naming the worker, the table and the behaviors that arm its
// deadlines, as the database's load marked its deadline queue.
func TestBehaviorWorkerNeedsTheRepositories(t *testing.T) {
	for name, tc := range map[string]struct {
		deps DepsInfo
		want string
	}{
		"sqlite first": {
			DepsInfo{Database: timersDB, Dialect: SQLiteDialect, ORM: "FixtureTimersDbOrm", DatabaseTypesPackage: "@schemas/fixture-timers-db-types"},
			"tsrestgen: worker TicketTimers runs Ticket's deadlines, which Timers arms, and fixture-timers-db lists sqlite first: the TypeScript repositories run behaviors on Postgres alone (D103)",
		},
		"no TypeScript types": {
			DepsInfo{Database: timersDB, Dialect: "postgres"},
			"tsrestgen: worker TicketTimers runs Ticket's deadlines, which Timers arms, through the repositories fixture-timers-db's TypeScript types export for Postgres (D103); enable outputs.types.typescript in fixture-timers-db's config",
		},
	} {
		t.Run(name, func(t *testing.T) {
			output := generateBehavior(t, timersFixture(t))
			// The operations set's own refusal comes first; the worker's
			// stands alone without it.
			var endpoints []EndpointInfo
			for _, ep := range output.Endpoints {
				if ep.Behavior == nil {
					endpoints = append(endpoints, ep)
				}
			}
			output.Endpoints = endpoints
			if err := output.SetDeps(tc.deps); err == nil || err.Error() != tc.want {
				t.Errorf("SetDeps = %v,\nwant %s", err, tc.want)
			}
		})
	}
}

// timersCheck is the file tsc checks the package for fixture-timers-api
// against beside it: ticketTimersQueues builds the worker's queues over a
// pool and the repositories, and buildRouter takes them.
const timersCheck = `import type { Pool } from 'pg';
import type { FixtureTimersDbOrm } from '@schemas/fixture-timers-db-types/orm';
import type { WorkerQueue } from '@superschematic/http-runtime/worker';
import { buildRouter, ticketTimersQueues, type Implementations } from './index';

declare const implementations: Implementations;
declare const orm: FixtureTimersDbOrm;
declare const pool: Pool;

export const router = buildRouter(implementations, { orm });
export const queues: WorkerQueue[] = ticketTimersQueues(pool, orm);
`

// TestGeneratedTimersAPICompiles type-checks the package for
// fixture-timers-api against its type packages, fixture-timers-db's
// repositories and queues among them, the real runtime and Hono, with
// timersCheck.
func TestGeneratedTimersAPICompiles(t *testing.T) {
	tree := materializeAPI(t, timersFixture(t))
	if err := os.WriteFile(filepath.Join(tree.apiDir, "timers.check.ts"), []byte(timersCheck), 0o644); err != nil {
		t.Fatal(err)
	}
	tree.typeCheck(t)
}

// timersCases are the cases behavior_timers_runtime.test.ts runs, by
// name: D103's Timers scenario, which the Go and Rust servers and workers
// run too, and the MCP endpoint's, which only the TypeScript server serves.
var timersCases = []string{
	"a create arms the deadline: its sub-table row and its queue message with its due time",
	"a due deadline the worker runs moves the status and records the outcome",
	"a status that leaves the state cancels the deadline by until",
	"a re-armed deadline does not fire on its old arming",
	"resetTimer and snoozeTimer are refused without the config's permission and run with it",
	"fireTimer through the SDK fires a due deadline and spends its message",
	"deleting the row removes its deadlines",
	"the MCP tool ticket_timers_fire_timer is listed with its schema, and calling it runs the operation",
}

// TestGeneratedTimersEndToEnd serves fixture-timers-api's generated router
// under bun over fixture-timers-db's generated repositories on Postgres, in
// a schema of its own at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL with the
// database's create.sql applied, drives it through the API's generated
// TypeScript SDK and its MCP endpoint, and runs the behavior worker's
// generated queues (ticketTimersQueues) in process under the runtime's
// runWorker (testdata/behavior_timers_runtime.test.ts): every case of
// timersCases must pass. It skips without the database, and without bun
// unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must run.
func TestGeneratedTimersEndToEnd(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated TypeScript server's and worker's Timers against Postgres")
	}
	fixture := timersFixture(t)
	tree := materializeAPI(t, fixture)
	tree.typeCheck(t)

	// The SDK, beside the package, resolving its types package by name.
	names := naming.Default()
	tsOutput, err := tsgen.Generate(fixture.schema, tsgen.Options{
		SchemaName:         timersAPI,
		Dependencies:       fixture.deps,
		DependencyPackages: map[string]string{timersDB: names.NpmTypesPackage(timersDB)},
		Registry:           fixture.registry,
		Clock:              fixedClock,
	})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	sdkOutput, err := sdkgen.Generate(fixture.endpoints, tsgen.ParseableTypeNames(tsOutput), fixedClock)
	if err != nil {
		t.Fatalf("sdkgen.Generate: %v", err)
	}
	sdkDir := filepath.Join(tree.root, "sdk", "typescript", timersAPI)
	if err := sdkgen.WriteSDKWithTools(sdkOutput, fixture.endpoints, sdkDir, fixedClock); err != nil {
		t.Fatalf("write the SDK: %v", err)
	}
	typesPackage := names.NpmTypesPackage(timersAPI)
	link(t, tree.typesDirs[typesPackage], filepath.Join(sdkDir, "node_modules", filepath.FromSlash(typesPackage)))

	ddl, err := sqlgen.Generate(fixture.deps[timersDB], sqlgen.Options{SchemaName: timersDB})
	if err != nil {
		t.Fatalf("generate %s's DDL: %v", timersDB, err)
	}
	ddlDir := filepath.Join(tree.root, "sql", timersDB)
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatalf("write %s's DDL: %v", timersDB, err)
	}
	createSQL := filepath.Join(ddlDir, "test-create.sql")
	pgtest.WriteCreateSQL(t, filepath.Join(ddlDir, "create.sql"), createSQL)

	testFile := tree.stage(t, "behavior_timers_runtime.test.ts")
	report := filepath.Join(tree.root, "behavior_timers.junit.xml")
	run := exec.Command(tree.bun, "test", testFile, "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = tree.apiDir
	run.Env = append(os.Environ(), "API_DIR="+tree.apiDir, "SDK_DIR="+sdkDir, "CREATE_SQL="+createSQL, "SUPERSCHEMATIC_RULES_TEST_DATABASE_URL="+dsn)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("behavior_timers_runtime.test.ts failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range timersCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(timersCases) {
		t.Errorf("bun ran %d cases, and timersCases names %d:\n%s", len(cases), len(timersCases), junit)
	}
	if testing.Verbose() {
		t.Logf("behavior_timers_runtime.test.ts:\n%s", out)
	}
}
