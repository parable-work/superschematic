package tsrestgen

import (
	"maps"
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

// schedulesAPI is the API that serves fixture-schedules-db's Problem (D104)
// for any signed-in caller: ProblemActions, the setSchedule,
// pauseSchedule, resumeSchedule, cancelSchedule, reportRun and
// listSchedules of the Schedules Problem implements and its status's
// transition; and the behavior worker ProblemWakeUps, which runs each
// problem's wake-ups as they fall due, the writes its Schedules hears, and
// the events its wake-ups emit, each handed to the project's Workers
// method problemWakeUpsSchedulesRun. The router serves its MCP tools.
const schedulesAPI = "fixture-schedules-api"

// schedulesDB is fixture-schedules-api's database, its one DB dependency.
const schedulesDB = "fixture-schedules-db"

// schedulesFixture is fixture-schedules-api with its database and apigen's
// endpoints for it, read with one registry, which declares the behaviors.
func schedulesFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, schedulesDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", schedulesDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, schedulesAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", schedulesAPI, err)
	}
	deps := map[string]*ir.Schema{schedulesDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     schedulesAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: schedulesAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, mcp: true, depsInfo: schedulesDeps}
}

// schedulesDeps is fixture-schedules-api's Deps as the dispatch layer sets
// it: a pool of its database, which lists postgres first, and the
// repositories its TypeScript types export.
var schedulesDeps = DepsInfo{
	Database:             schedulesDB,
	Dialect:              "postgres",
	ORM:                  "FixtureSchedulesDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-schedules-db-types",
}

// generateSchedulesFixture generates the package for fixture-schedules-api,
// its Deps set.
func generateSchedulesFixture(t *testing.T) *APIOutput {
	t.Helper()
	output := generateBehavior(t, schedulesFixture(t))
	if err := output.SetDeps(schedulesDeps); err != nil {
		t.Fatalf("SetDeps: %v", err)
	}
	return output
}

// TestWriteAPIGoldenBehaviorSchedules pins the package for
// fixture-schedules-api: its router mounts Problem's Schedules operations
// under schedules and its status's transition under status; deps.ts's
// Workers has the method problemWakeUpsSchedulesRun, which takes the event
// run of Problem's Schedules with the repositories in its transaction; the
// scaffold's createWorkers stubs it; and behavior-workers.ts exports
// ProblemWakeUps' queues, its deadlines, changes and events, the events
// handed to workers by problemWakeUpsEvent. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenBehaviorSchedules -update
func TestWriteAPIGoldenBehaviorSchedules(t *testing.T) {
	output := generateSchedulesFixture(t)
	checkGolden(t, output, schedulesAPI)
	dir := t.TempDir()
	if err := WriteAPI(output, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, BehaviorWorkersFile))
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "golden", schedulesAPI, BehaviorWorkersFile)
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

// TestGenerateSchedulesOperations: each behavior operation of
// fixture-schedules-db's Problem calls the repository under its use's
// member: schedules for the Schedules Problem implements, status for its
// Workflow. No use takes a precondition. The package has one behavior
// worker, ProblemWakeUps, over Problem's deadline, change and event
// queues, holding problems.schedule, whose one event is a method of
// Workers; so deps.ts declares Workers, and the scaffold builds them.
func TestGenerateSchedulesOperations(t *testing.T) {
	output := generateSchedulesFixture(t)
	if !output.TableBehaviors || !output.HasBehaviors() || !output.HasBehaviorWorkers() || output.BehaviorsTakePreconditions() {
		t.Errorf("TableBehaviors %t, HasBehaviors %t, HasBehaviorWorkers %t, BehaviorsTakePreconditions %t", output.TableBehaviors, output.HasBehaviors(), output.HasBehaviorWorkers(), output.BehaviorsTakePreconditions())
	}
	routes := map[string]*BehaviorRoute{}
	for _, route := range output.BehaviorRoutes() {
		routes[route.Type+"."+route.Use+"."+route.Operation] = route
	}
	for _, name := range []string{"setSchedule", "pauseSchedule", "resumeSchedule", "cancelSchedule", "reportRun", "listSchedules"} {
		route := routes["Problem.Schedules."+name]
		if route == nil || route.Method() != "problem.schedules."+name {
			t.Errorf("Problem.Schedules.%s calls %+v, want problem.schedules.%s", name, route, name)
		}
	}
	if route := routes["Problem.status.transition"]; route == nil || route.Method() != "problem.status.transition" {
		t.Errorf("Problem.status.transition calls %+v", route)
	}
	if len(routes) != 7 {
		t.Errorf("the router mounts %d behavior operations, want 7: %v", len(routes), slices.Sorted(maps.Keys(routes)))
	}

	workers := output.BehaviorWorkers()
	if len(workers) != 1 {
		t.Fatalf("behavior workers = %d, want ProblemWakeUps alone", len(workers))
	}
	w := workers[0]
	if w.Name != "ProblemWakeUps" || w.Behavior.Table != "Problem" || w.Behavior.DeadlineQueue != "ProblemDeadline" || w.Behavior.ChangeQueue != "ProblemChange" || w.Behavior.EventQueue != "ProblemEvent" || !slices.Equal(w.Behavior.Permissions, []string{"problems.schedule"}) {
		t.Errorf("ProblemWakeUps = %s %+v", w.Name, *w.Behavior)
	}
	if !w.Emits() || w.EventQueueConst() != "problemEventQueue" || w.EventFunc() != "problemWakeUpsEvent" || w.QueueKinds() != "deadlines, changes and events" {
		t.Errorf("ProblemWakeUps emits %t, its event queue %q, its dispatcher %q, its queues %q", w.Emits(), w.EventQueueConst(), w.EventFunc(), w.QueueKinds())
	}
	events := output.WorkerEvents()
	if len(events) != 1 {
		t.Fatalf("worker events = %+v, want the one run of Problem's Schedules", events)
	}
	if e := events[0]; e.Use != "Schedules" || e.Kind != "run" || e.TSMethod() != "problemWakeUpsSchedulesRun" || e.Event != "ProblemSchedulesRunEvent" || e.Payload != "ProblemSchedulesRunPayload" || e.Queue != "ProblemEvent" || e.Subject != "fixture-schedules-api/ProblemWakeUps" {
		t.Errorf("the event = %+v", e)
	}
	if !output.HasWorkers() || len(output.OrdinaryWorkers()) != 0 {
		t.Errorf("HasWorkers %t, ordinary workers %d: the API's Workers is the event's handler alone", output.HasWorkers(), len(output.OrdinaryWorkers()))
	}

	dir := t.TempDir()
	if err := WriteAPI(output, dir); err != nil {
		t.Fatal(err)
	}
	for file, wants := range map[string][]string{
		"router.ts": {"orm.problem.schedules.setSchedule(", "orm.problem.schedules.listSchedules(", "orm.problem.status.transition("},
		"deps.ts": {
			"problemWakeUpsSchedulesRun(event: ProblemSchedulesRunEvent, tx: FixtureSchedulesDbTx, signal: AbortSignal): Promise<void>;",
			"import type { ProblemSchedulesRunEvent } from '@schemas/fixture-schedules-api-types/types';",
			"export type WorkersConstructor",
		},
		BehaviorWorkersFile: {
			"export function problemWakeUpsQueues(pool: Pool, orm: FixtureSchedulesDbOrm, workers: Workers, logger?: Logger): WorkerQueue[] {",
			".withBehaviorCaller(problemWakeUpsCaller)\n              .transaction(tx => tx.problem.runEvent(claim.message, { claim: { id: claim.id, token: claim.token } }, () => problemWakeUpsEvent(workers, tx, claim.message, signal)))",
			"if (message.useId === 'Schedules' && message.kind === 'run') {",
			"no Workers method takes event ${message.kind} of ${message.useId} on Problem ${message.rowKey} (D104)",
		},
	} {
		text, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(text), want) {
				t.Errorf("%s has no %s", file, want)
			}
		}
	}
}

// schedulesCheck is the file tsc checks the package for
// fixture-schedules-api against beside it: the router takes the
// repositories; the worker's queues are built over a pool, them and the
// project's Workers, whose one method takes the event run of Problem's
// Schedules, typed by its payloadSchema, with the repositories in its
// transaction (FixtureSchedulesDbTx); and the scaffold's createWorkers is
// a WorkersConstructor.
const schedulesCheck = `import type { Pool } from 'pg';
import type { FixtureSchedulesDbOrm, FixtureSchedulesDbTx } from '@schemas/fixture-schedules-db-types/orm';
import type { ProblemSchedulesRunEvent } from '@schemas/fixture-schedules-api-types/types';
import type { WorkerQueue } from '@superschematic/http-runtime/worker';
import { buildRouter, problemWakeUpsQueues, type Implementations, type Workers, type WorkersConstructor } from './index';

declare const implementations: Implementations;
declare const orm: FixtureSchedulesDbOrm;
declare const pool: Pool;
declare const problem: Parameters<FixtureSchedulesDbOrm['problem']['getOne']>[0];

export const router = buildRouter(implementations, { orm });
export const workers: Workers = {
  async problemWakeUpsSchedulesRun(event: ProblemSchedulesRunEvent, tx: FixtureSchedulesDbTx) {
    const run: number = event.payload.run;
    const attempt: number = event.payload.attempt;
    const kind: 'after' | 'every' | 'cron' = event.payload.kind;
    const due: number = event.payload.dueAt;
    const next: number | null | undefined = event.payload.nextAt;
    void [attempt, kind, due, next];
    await tx.problem.schedules.reportRun(event.key, { name: event.payload.name, run, outcome: 'told' });
  },
};
export const queues: WorkerQueue[] = problemWakeUpsQueues(pool, orm, workers);
export const set = orm.problem.schedules.setSchedule(problem, { name: 'nudge', after: 300000 });
// @ts-expect-error lost is no outcome of Problem's Schedules.
export const lost = orm.problem.schedules.reportRun(problem, { name: 'nudge', run: 1, outcome: 'lost' });
// @ts-expect-error Workers has the method of the event run of Problem's Schedules.
export const none: Workers = {};
export const construct: WorkersConstructor = () => workers;
`

// TestGeneratedSchedulesAPICompiles type-checks the package for
// fixture-schedules-api against its type packages, fixture-schedules-db's
// repositories and queues and the API's own types, the event's among
// them, the real runtime and Hono, with schedulesCheck.
func TestGeneratedSchedulesAPICompiles(t *testing.T) {
	tree := materializeAPI(t, schedulesFixture(t))
	if err := os.WriteFile(filepath.Join(tree.apiDir, "schedules.check.ts"), []byte(schedulesCheck), 0o644); err != nil {
		t.Fatal(err)
	}
	tree.typeCheck(t)
}

// schedulesCases are the cases behavior_schedules_runtime.test.ts runs, by
// name: D104's end-to-end cases of Schedules on a table and of its events'
// handler, which the Go and Rust servers and workers run too, in one
// wording. The TypeScript server's MCP endpoint is held within the case of
// a repeated set.
var schedulesCases = []string{
	"setSchedule arms a wake-up after a delay, at an interval and on a cron line in the config's time zone, each a deadline message due at its nextAt",
	"a repeated setSchedule of a live name with the same spec answers unchanged and leaves the row's _version where it was",
	"setSchedule without problems.schedule is 403 forbidden, a delay under minMs is 400 invalid_argument, and an eleventh live wake-up is 409 schedule_limit, each leaving the row's _version where it was",
	"a due wake-up fires on the worker as run 1 and hands the project's handler one run event with its name, run, attempt, kind, spec and times, which reports told",
	"the handler runs once per event: a completed event's message is gone and is not handed to it again",
	"a handler that fails fails its message, which is due again after the queue's backoff, and the next claim hands it to the handler again",
	"an interval overdue by many runs fires once, and its next run is the first of its grid after the fire",
	"a missed run is tried again five minutes on, as attempt 2 of the same run",
	"a report of an older run answers unchanged, and one of a run not had is 409 no_run",
	"pauseSchedule keeps nextAt and deletes its deadline message, and resumeSchedule past nextAt fires at once, once",
	"cancelSchedule finishes a wake-up and frees its place under max",
	"a problem whose status enters done finishes its wake-ups, and a new one is 409 schedules_ended",
	"a raw SQL write of a problem's status to dropped is heard by the worker and finishes its wake-ups",
	"listSchedules reads a problem's wake-ups in name order",
}

// TestGeneratedSchedulesEndToEnd serves fixture-schedules-api's generated
// router under bun over fixture-schedules-db's generated repositories on
// Postgres, in a schema of its own at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL
// with the database's create.sql applied, drives the behavior operations
// through the API's generated TypeScript SDK and its MCP endpoint, and runs
// the behavior worker's generated queues (problemWakeUpsQueues: Problem's
// deadlines, changes and events) in process under the runtime's runWorker
// with a Workers of the test's own, which records each event and reports
// its run, a deadline made due with SQL
// (testdata/behavior_schedules_runtime.test.ts): every case of
// schedulesCases must pass. It skips without the database, and without bun
// unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must run.
func TestGeneratedSchedulesEndToEnd(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated TypeScript server's and worker's Schedules and its events' handler against Postgres")
	}
	fixture := schedulesFixture(t)
	tree := materializeAPI(t, fixture)
	tree.typeCheck(t)

	// The SDK, beside the package, resolving its types package by name.
	names := naming.Default()
	tsOutput, err := tsgen.Generate(fixture.schema, tsgen.Options{
		SchemaName:         schedulesAPI,
		Dependencies:       fixture.deps,
		DependencyPackages: map[string]string{schedulesDB: names.NpmTypesPackage(schedulesDB)},
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
	sdkDir := filepath.Join(tree.root, "sdk", "typescript", schedulesAPI)
	if err := sdkgen.WriteSDKWithTools(sdkOutput, fixture.endpoints, sdkDir, fixedClock); err != nil {
		t.Fatalf("write the SDK: %v", err)
	}
	typesPackage := names.NpmTypesPackage(schedulesAPI)
	link(t, tree.typesDirs[typesPackage], filepath.Join(sdkDir, "node_modules", filepath.FromSlash(typesPackage)))

	ddl, err := sqlgen.Generate(fixture.deps[schedulesDB], sqlgen.Options{SchemaName: schedulesDB})
	if err != nil {
		t.Fatalf("generate %s's DDL: %v", schedulesDB, err)
	}
	ddlDir := filepath.Join(tree.root, "sql", schedulesDB)
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatalf("write %s's DDL: %v", schedulesDB, err)
	}
	createSQL := filepath.Join(ddlDir, "test-create.sql")
	pgtest.WriteCreateSQL(t, filepath.Join(ddlDir, "create.sql"), createSQL)

	testFile := tree.stage(t, "behavior_schedules_runtime.test.ts")
	report := filepath.Join(tree.root, "behavior_schedules.junit.xml")
	run := exec.Command(tree.bun, "test", testFile, "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = tree.apiDir
	run.Env = append(os.Environ(), "API_DIR="+tree.apiDir, "SDK_DIR="+sdkDir, "CREATE_SQL="+createSQL, "SUPERSCHEMATIC_RULES_TEST_DATABASE_URL="+dsn)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("behavior_schedules_runtime.test.ts failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range schedulesCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(schedulesCases) {
		t.Errorf("bun ran %d cases, and schedulesCases names %d:\n%s", len(cases), len(schedulesCases), junit)
	}
	if testing.Verbose() {
		t.Logf("behavior_schedules_runtime.test.ts:\n%s", out)
	}
}
