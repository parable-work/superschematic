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

// seatsAPI is the API that serves fixture-seats-db's tables (D104) for any
// signed-in caller: ProblemActions, the takeSeat, leaveSeat and listSeats
// of the Seats Problem implements and its status's transition, whose by
// names seats; SandboxActions, Sandbox's status transition and its
// observed field's observe, a Reconcile; and the behavior worker
// SandboxDeadlines, which runs each sandbox's stale deadline. Seats and
// Reconcile are the first behaviors only the behaviors' core implements.
// The router serves its MCP tools.
const seatsAPI = "fixture-seats-api"

// seatsDB is fixture-seats-api's database, its one DB dependency.
const seatsDB = "fixture-seats-db"

// seatsFixture is fixture-seats-api with its database and apigen's
// endpoints for it, read with one registry, which declares the behaviors.
func seatsFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, seatsDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", seatsDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, seatsAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", seatsAPI, err)
	}
	deps := map[string]*ir.Schema{seatsDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     seatsAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: seatsAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, mcp: true, depsInfo: seatsDeps}
}

// seatsDeps is fixture-seats-api's Deps as the dispatch layer sets it: a
// pool of its database, which lists postgres first, and the repositories
// its TypeScript types export.
var seatsDeps = DepsInfo{
	Database:             seatsDB,
	Dialect:              "postgres",
	ORM:                  "FixtureSeatsDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-seats-db-types",
}

// generateSeatsFixture generates the package for fixture-seats-api, its
// Deps set.
func generateSeatsFixture(t *testing.T) *APIOutput {
	t.Helper()
	output := generateBehavior(t, seatsFixture(t))
	if err := output.SetDeps(seatsDeps); err != nil {
		t.Fatalf("SetDeps: %v", err)
	}
	return output
}

// TestWriteAPIGoldenBehaviorSeats pins the package for fixture-seats-api:
// its router mounts Problem's Seats operations under seats and its
// status's transition under status, and Sandbox's transition and observe
// under its fields, each handing the repository its parameters; and
// behavior-workers.ts exports SandboxDeadlines' queues. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenBehaviorSeats -update
func TestWriteAPIGoldenBehaviorSeats(t *testing.T) {
	output := generateSeatsFixture(t)
	checkGolden(t, output, seatsAPI)
	dir := t.TempDir()
	if err := WriteAPI(output, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, BehaviorWorkersFile))
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "golden", seatsAPI, BehaviorWorkersFile)
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

// TestGenerateSeatsOperations: each behavior operation of fixture-seats-db's
// tables calls the repository under its use's member: seats for the Seats
// Problem implements, the field for Problem's and Sandbox's Workflows and
// for Sandbox's Reconcile, which a field anchors as Workflow does. No use
// takes a precondition, so no input has the member; takeSeat's and
// leaveSeat's subject and listSeats' seat are optional, and observe's
// members are not. The package has one behavior worker, over Sandbox's
// deadline queue, which needs no permission.
func TestGenerateSeatsOperations(t *testing.T) {
	output := generateSeatsFixture(t)
	if !output.TableBehaviors || !output.HasBehaviors() || !output.HasBehaviorWorkers() || output.BehaviorsTakePreconditions() || output.BehaviorsOnFields() {
		t.Errorf("TableBehaviors %t, HasBehaviors %t, HasBehaviorWorkers %t, BehaviorsTakePreconditions %t, BehaviorsOnFields %t", output.TableBehaviors, output.HasBehaviors(), output.HasBehaviorWorkers(), output.BehaviorsTakePreconditions(), output.BehaviorsOnFields())
	}
	routes := map[string]*BehaviorRoute{}
	for _, route := range output.BehaviorRoutes() {
		routes[route.Type+"."+route.Use+"."+route.Operation] = route
	}
	for name, want := range map[string]struct {
		method   string
		optional []string
	}{
		"Problem.Seats.takeSeat":    {"problem.seats.takeSeat", []string{"subject"}},
		"Problem.Seats.leaveSeat":   {"problem.seats.leaveSeat", []string{"subject"}},
		"Problem.Seats.listSeats":   {"problem.seats.listSeats", []string{"seat"}},
		"Problem.status.transition": {"problem.status.transition", []string{"from"}},
		"Sandbox.status.transition": {"sandbox.status.transition", []string{"from"}},
		"Sandbox.observed.observe":  {"sandbox.observed.observe", nil},
	} {
		route := routes[name]
		if route == nil || route.Method() != want.method {
			t.Errorf("%s calls %+v, want %q", name, route, want.method)
			continue
		}
		if route.Preconditions {
			t.Errorf("%s takes preconditions, which no use of %s takes", name, route.Type)
		}
		if !slices.Equal(route.Optional, want.optional) {
			t.Errorf("%s's optional members = %v, want %v", name, route.Optional, want.optional)
		}
	}
	if len(routes) != 6 {
		t.Errorf("the router mounts %d behavior operations, want 6: %v", len(routes), slices.Sorted(maps.Keys(routes)))
	}

	workers := output.BehaviorWorkers()
	if len(workers) != 1 {
		t.Fatalf("behavior workers = %d, want SandboxDeadlines alone", len(workers))
	}
	sandbox := workers[0]
	if sandbox.Name != "SandboxDeadlines" || sandbox.Behavior.Table != "Sandbox" || sandbox.Behavior.DeadlineQueue != "SandboxDeadline" || len(sandbox.Behavior.Permissions) != 0 {
		t.Errorf("SandboxDeadlines = %s %+v", sandbox.Name, *sandbox.Behavior)
	}

	dir := t.TempDir()
	if err := WriteAPI(output, dir); err != nil {
		t.Fatal(err)
	}
	router, err := os.ReadFile(filepath.Join(dir, "router.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"orm.problem.seats.takeSeat(",
		"orm.problem.seats.listSeats(",
		"orm.sandbox.observed.observe(",
	} {
		if !strings.Contains(string(router), want) {
			t.Errorf("router.ts has no %s", want)
		}
	}
	if strings.Contains(string(router), "behaviorPreconditions") {
		t.Errorf("router.ts presents preconditions, which no use of fixture-seats-db takes")
	}
}

// seatsCheck is the file tsc checks the package for fixture-seats-api
// against beside it: the router takes the repositories, the worker's queues
// are built over a pool and them, and the repositories type Seats' seats
// by the config's names and Sandbox's observed and observe's state by its
// Workflow's states.
const seatsCheck = `import type { Pool } from 'pg';
import type { FixtureSeatsDbOrm, SandboxRow } from '@schemas/fixture-seats-db-types/orm';
import type { WorkerQueue } from '@superschematic/http-runtime/worker';
import { buildRouter, sandboxDeadlinesQueues, type Implementations } from './index';

declare const implementations: Implementations;
declare const orm: FixtureSeatsDbOrm;
declare const pool: Pool;
declare const problem: Parameters<FixtureSeatsDbOrm['problem']['getOne']>[0];
declare const sandbox: Parameters<FixtureSeatsDbOrm['sandbox']['getOne']>[0];

export const router = buildRouter(implementations, { orm });
export const queues: WorkerQueue[] = sandboxDeadlinesQueues(pool, orm);
export const taken = orm.problem.seats.takeSeat(problem, { seat: 'accountable' });
export const left = orm.problem.seats.leaveSeat(problem, { seat: 'handles', subject: 'someone' });
export const listed = orm.problem.seats.listSeats(problem, {});
// @ts-expect-error Problem's Seats has no seat owners.
export const unknownSeat = orm.problem.seats.takeSeat(problem, { seat: 'owners' });
export const observed = orm.sandbox.observed.observe(sandbox, { state: 'running', at: 0 });
// @ts-expect-error Sandbox's Workflow has no state hibernating.
export const unknownState = orm.sandbox.observed.observe(sandbox, { state: 'hibernating', at: 0 });
export const state: SandboxRow['observed'] = 'unreachable';
export const unobserved: SandboxRow['observed'] = null;
// @ts-expect-error A row's observed is one of its Workflow's states.
export const lost: SandboxRow['observed'] = 'lost';
`

// TestGeneratedSeatsAPICompiles type-checks the package for
// fixture-seats-api against its type packages, fixture-seats-db's
// repositories and queues among them, the real runtime and Hono, with
// seatsCheck.
func TestGeneratedSeatsAPICompiles(t *testing.T) {
	tree := materializeAPI(t, seatsFixture(t))
	if err := os.WriteFile(filepath.Join(tree.apiDir, "seats.check.ts"), []byte(seatsCheck), 0o644); err != nil {
		t.Fatal(err)
	}
	tree.typeCheck(t)
}

// seatsCases are the cases behavior_seats_runtime.test.ts runs, by name:
// D104's end-to-end cases of Seats, of a by naming a seat and of Reconcile
// on a table, which the Go and Rust servers and workers run too, in one
// wording. The TypeScript server's MCP endpoint is held within the cases
// of a repeated take and of a repeated observation.
var seatsCases = []string{
	"a caller holding take seats itself as accountable, and the next to take the seat displaces it (onFull displace)",
	"a take of a seat the caller holds already answers unchanged and leaves the row's _version where it was",
	"a take by a caller without the seat's take permission is 403 forbidden",
	"the row's agent is seated as responsible through its field: listSeats lists it first, and its leaveSeat is 409 held_by_field",
	"a sixth handle is 409 seat_full",
	"a handle is released by the agent that claimed it or by a caller holding assign, and another caller's release is 403 forbidden",
	"a leave of a seat the caller does not hold answers unchanged",
	"a transition whose by names seat:responsible is made by a subject seated there, and another caller is 409 not_principal naming the seat",
	"a caller holding overridePermission moves past by",
	"in review the accountable subject edits the title StateRules freezes, and another caller's edit is 409 frozen_fields naming seat:accountable",
	"observe moves the sandbox along the fewest transitions to the observed state and records observed, observedAt and reconciledAt",
	"an observation no newer than observedAt answers unchanged and leaves the row's _version where it was",
	"a step the observer may not make stops the path and records drift as forbidden, and an observer who may completes it and clears drift",
	"observe without observePermission is 403 forbidden, and a state the Workflow lacks is 400 invalid_argument",
	"the stale deadline falls due on the worker five minutes after the last observation and moves the sandbox to unreachable",
	"a newer observation moves the sandbox back from unreachable",
}

// TestGeneratedSeatsEndToEnd serves fixture-seats-api's generated router
// under bun over fixture-seats-db's generated repositories on Postgres, in
// a schema of its own at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL with the
// database's create.sql applied, drives the behavior operations through
// the API's generated TypeScript SDK and its MCP endpoint, the creates and
// updates through the repositories as a behavior caller
// (withBehaviorCaller), and runs the behavior worker's generated queues
// (sandboxDeadlinesQueues) in process under the runtime's runWorker, a
// stale deadline made due with SQL (testdata/behavior_seats_runtime.test.ts):
// every case of seatsCases must pass. It skips without the database, and
// without bun unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must
// run.
func TestGeneratedSeatsEndToEnd(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated TypeScript server's and worker's Seats and Reconcile against Postgres")
	}
	fixture := seatsFixture(t)
	tree := materializeAPI(t, fixture)
	tree.typeCheck(t)

	// The SDK, beside the package, resolving its types package by name.
	names := naming.Default()
	tsOutput, err := tsgen.Generate(fixture.schema, tsgen.Options{
		SchemaName:         seatsAPI,
		Dependencies:       fixture.deps,
		DependencyPackages: map[string]string{seatsDB: names.NpmTypesPackage(seatsDB)},
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
	sdkDir := filepath.Join(tree.root, "sdk", "typescript", seatsAPI)
	if err := sdkgen.WriteSDKWithTools(sdkOutput, fixture.endpoints, sdkDir, fixedClock); err != nil {
		t.Fatalf("write the SDK: %v", err)
	}
	typesPackage := names.NpmTypesPackage(seatsAPI)
	link(t, tree.typesDirs[typesPackage], filepath.Join(sdkDir, "node_modules", filepath.FromSlash(typesPackage)))

	ddl, err := sqlgen.Generate(fixture.deps[seatsDB], sqlgen.Options{SchemaName: seatsDB})
	if err != nil {
		t.Fatalf("generate %s's DDL: %v", seatsDB, err)
	}
	ddlDir := filepath.Join(tree.root, "sql", seatsDB)
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatalf("write %s's DDL: %v", seatsDB, err)
	}
	createSQL := filepath.Join(ddlDir, "test-create.sql")
	pgtest.WriteCreateSQL(t, filepath.Join(ddlDir, "create.sql"), createSQL)

	testFile := tree.stage(t, "behavior_seats_runtime.test.ts")
	report := filepath.Join(tree.root, "behavior_seats.junit.xml")
	run := exec.Command(tree.bun, "test", testFile, "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = tree.apiDir
	run.Env = append(os.Environ(), "API_DIR="+tree.apiDir, "SDK_DIR="+sdkDir, "CREATE_SQL="+createSQL, "SUPERSCHEMATIC_RULES_TEST_DATABASE_URL="+dsn)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("behavior_seats_runtime.test.ts failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range seatsCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(seatsCases) {
		t.Errorf("bun ran %d cases, and seatsCases names %d:\n%s", len(cases), len(seatsCases), junit)
	}
	if testing.Verbose() {
		t.Logf("behavior_seats_runtime.test.ts:\n%s", out)
	}
}
