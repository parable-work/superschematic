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

// poolAPI is the API that serves fixture-pool-db's Account (D104) for any
// signed-in caller: AccountActions, the markOut and reportRoom of the Pool
// Account implements, its pick, an operation of the table as a whole routed
// at account/pool/pick with no key, and its Budget's operations; and the
// behavior worker AccountRechecks, which runs each out account's recheck as
// it falls due and hands each recheck event to the project's Workers method
// accountRechecksPoolRecheck. The router serves its MCP tools.
const poolAPI = "fixture-pool-api"

// poolDB is fixture-pool-api's database, its one DB dependency.
const poolDB = "fixture-pool-db"

// poolFixture is fixture-pool-api with its database and apigen's endpoints
// for it, read with one registry, which declares the behaviors.
func poolFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, poolDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", poolDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, poolAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", poolAPI, err)
	}
	deps := map[string]*ir.Schema{poolDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     poolAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: poolAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, mcp: true, depsInfo: poolDeps}
}

// poolDeps is fixture-pool-api's Deps as the dispatch layer sets it: a pool
// of its database, which lists postgres first, and the repositories its
// TypeScript types export.
var poolDeps = DepsInfo{
	Database:             poolDB,
	Dialect:              "postgres",
	ORM:                  "FixturePoolDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-pool-db-types",
}

// generatePoolFixture generates the package for fixture-pool-api, its Deps
// set.
func generatePoolFixture(t *testing.T) *APIOutput {
	t.Helper()
	output := generateBehavior(t, poolFixture(t))
	if err := output.SetDeps(poolDeps); err != nil {
		t.Fatalf("SetDeps: %v", err)
	}
	return output
}

// TestWriteAPIGoldenBehaviorPool pins the package for fixture-pool-api: its
// router mounts Account's Pool operations under pool, pick with no key, and
// its Budget's under budget; deps.ts's Workers has the method
// accountRechecksPoolRecheck, which takes the event recheck of Account's
// Pool with the repositories in its transaction; the scaffold's
// createWorkers stubs it; and behavior-workers.ts exports AccountRechecks'
// queues, its deadlines and events, the events handed to workers by
// accountRechecksEvent. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenBehaviorPool -update
func TestWriteAPIGoldenBehaviorPool(t *testing.T) {
	output := generatePoolFixture(t)
	checkGolden(t, output, poolAPI)
	dir := t.TempDir()
	if err := WriteAPI(output, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, BehaviorWorkersFile))
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "golden", poolAPI, BehaviorWorkersFile)
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

// TestGeneratePoolOperations: each behavior operation of fixture-pool-db's
// Account calls the repository under its use's member, pool for the Pool
// Account implements and budget for its Budget; pick, an operation of the
// table as a whole (D104), is routed with no key and takes its optional
// members' nulls out, and no operation takes a precondition. The package
// has one behavior worker, AccountRechecks, over Account's deadline and
// event queues, holding accounts.pool, whose one event is a method of
// Workers; so deps.ts declares Workers, and the scaffold builds them.
func TestGeneratePoolOperations(t *testing.T) {
	output := generatePoolFixture(t)
	if !output.TableBehaviors || !output.HasBehaviors() || !output.HasBehaviorWorkers() || output.BehaviorsTakePreconditions() {
		t.Errorf("TableBehaviors %t, HasBehaviors %t, HasBehaviorWorkers %t, BehaviorsTakePreconditions %t", output.TableBehaviors, output.HasBehaviors(), output.HasBehaviorWorkers(), output.BehaviorsTakePreconditions())
	}
	routes := map[string]*BehaviorRoute{}
	for _, route := range output.BehaviorRoutes() {
		routes[route.Type+"."+route.Use+"."+route.Operation] = route
	}
	for _, name := range []string{"markOut", "reportRoom", "pick"} {
		route := routes["Account.Pool."+name]
		if route == nil || route.Method() != "account.pool."+name {
			t.Errorf("Account.Pool.%s calls %+v, want account.pool.%s", name, route, name)
			continue
		}
		if table := name == "pick"; route.Table != table || (route.Key.Name == "") != table {
			t.Errorf("Account.Pool.%s is on the table %t with key %q, want on the table %t", name, route.Table, route.Key.Name, table)
		}
	}
	if pick := routes["Account.Pool.pick"]; pick != nil && !slices.Equal(pick.Optional, []string{"exclude", "match", "prefer"}) {
		t.Errorf("pick's optional members = %v, want [exclude match prefer]", pick.Optional)
	}
	budget := 0
	for name, route := range routes {
		if strings.HasPrefix(name, "Account.Budget.") {
			budget++
			if route.Table || route.Method() != "account.budget."+route.Operation {
				t.Errorf("%s calls %s on the table %t", name, route.Method(), route.Table)
			}
		}
	}
	if len(routes) != 3+budget || budget == 0 {
		t.Errorf("the router mounts %d behavior operations, Budget's %d: %v", len(routes), budget, slices.Sorted(maps.Keys(routes)))
	}

	workers := output.BehaviorWorkers()
	if len(workers) != 1 {
		t.Fatalf("behavior workers = %d, want AccountRechecks alone", len(workers))
	}
	w := workers[0]
	if w.Name != "AccountRechecks" || w.Behavior.Table != "Account" || w.Behavior.DeadlineQueue != "AccountDeadline" || w.Behavior.ChangeQueue != "" || w.Behavior.EventQueue != "AccountEvent" || !slices.Equal(w.Behavior.Permissions, []string{"accounts.pool"}) {
		t.Errorf("AccountRechecks = %s %+v", w.Name, *w.Behavior)
	}
	if !w.Emits() || w.EventQueueConst() != "accountEventQueue" || w.EventFunc() != "accountRechecksEvent" || w.QueueKinds() != "deadlines and events" {
		t.Errorf("AccountRechecks emits %t, its event queue %q, its dispatcher %q, its queues %q", w.Emits(), w.EventQueueConst(), w.EventFunc(), w.QueueKinds())
	}
	events := output.WorkerEvents()
	if len(events) != 1 {
		t.Fatalf("worker events = %+v, want the one recheck of Account's Pool", events)
	}
	if e := events[0]; e.Use != "Pool" || e.Kind != "recheck" || e.TSMethod() != "accountRechecksPoolRecheck" || e.Event != "AccountPoolRecheckEvent" || e.Payload != "AccountPoolRecheckPayload" || e.Queue != "AccountEvent" || e.Subject != "fixture-pool-api/AccountRechecks" {
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
		"router.ts": {
			"orm.account.pool.markOut(\n          request.path['id'] as",
			"options.orm.account.pool.pick(\n          // The repository's parameters: the wire values of AccountPoolPickInput,\n          // each optional member that is null left out (D104).\n          behaviorParams(request.input, ['exclude', 'match', 'prefer']) as never,\n          { caller: behaviorCaller(ctx) }\n        )",
			"a Pool's operation of the table as a\n  // whole (D104)",
		},
		"deps.ts": {
			"accountRechecksPoolRecheck(event: AccountPoolRecheckEvent, tx: FixturePoolDbTx, signal: AbortSignal): Promise<void>;",
			"import type { AccountPoolRecheckEvent } from '@schemas/fixture-pool-api-types/types';",
			"export type WorkersConstructor",
		},
		BehaviorWorkersFile: {
			"export function accountRechecksQueues(pool: Pool, orm: FixturePoolDbOrm, workers: Workers, logger?: Logger): WorkerQueue[] {",
			"if (message.useId === 'Pool' && message.kind === 'recheck') {",
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

// poolCheck is the file tsc checks the package for fixture-pool-api against
// beside it: the router takes the repositories; the worker's queues are
// built over a pool, them and the project's Workers, whose one method takes
// the event recheck of Account's Pool, typed by its payloadSchema, with the
// repositories in its transaction (FixturePoolDbTx), and reports a check
// through it; pick, an operation of the table as a whole, takes no key and
// no preconditions; and the scaffold's createWorkers is a
// WorkersConstructor.
const poolCheck = `import type { Pool } from 'pg';
import type { FixturePoolDbOrm, FixturePoolDbTx } from '@schemas/fixture-pool-db-types/orm';
import type { AccountPoolRecheckEvent } from '@schemas/fixture-pool-api-types/types';
import type { WorkerQueue } from '@superschematic/http-runtime/worker';
import { accountRechecksQueues, buildRouter, type Implementations, type Workers, type WorkersConstructor } from './index';

declare const implementations: Implementations;
declare const orm: FixturePoolDbOrm;
declare const pool: Pool;
declare const account: Parameters<FixturePoolDbOrm['account']['getOne']>[0];

export const router = buildRouter(implementations, { orm });
export const workers: Workers = {
  async accountRechecksPoolRecheck(event: AccountPoolRecheckEvent, tx: FixturePoolDbTx) {
    const due: number = event.payload.dueAt;
    const out: number = event.payload.outUntil;
    const reason: string | null | undefined = event.payload.outReason;
    void [due, out, reason];
    await tx.account.pool.reportRoom(event.key, { room: false, resetsAt: due + 60000, reason: 'busy' });
  },
};
export const queues: WorkerQueue[] = accountRechecksQueues(pool, orm, workers);
export const out = orm.account.pool.markOut(account, { reason: 'rate limited' });
export const picked = orm.account.pool.pick({ match: { vendor: 'anthropic' }, prefer: { kind: 'account' }, exclude: ['a1'] }).then(answer => (answer.member === null ? answer.until : answer.member.order));
// @ts-expect-error pick runs on the table as a whole: it takes no key.
export const keyed = orm.account.pool.pick(account, {});
// @ts-expect-error Workers has the method of the event recheck of Account's Pool.
export const none: Workers = {};
export const construct: WorkersConstructor = () => workers;
`

// TestGeneratedPoolAPICompiles type-checks the package for fixture-pool-api
// against its type packages, fixture-pool-db's repositories and queues and
// the API's own types, the event's among them, the real runtime and Hono,
// with poolCheck.
func TestGeneratedPoolAPICompiles(t *testing.T) {
	tree := materializeAPI(t, poolFixture(t))
	if err := os.WriteFile(filepath.Join(tree.apiDir, "pool.check.ts"), []byte(poolCheck), 0o644); err != nil {
		t.Fatal(err)
	}
	tree.typeCheck(t)
}

// poolCases are the cases behavior_pool_runtime.test.ts runs, by name:
// D104's end-to-end cases of Pool on a table, of its row-less pick and of
// its recheck events' handler, which the Go and Rust servers and workers
// run too, in one wording; the last, the MCP tool's, is the TypeScript
// server's alone.
var poolCases = []string{
	"a pick answers the open account lowest by rank among an owner's accounts",
	"a null member of match or prefer is absent, so a pick keeps an owner's accounts whatever their rank",
	"prefer tries a vendor's open accounts, then its near ones, then the others by rank",
	"markOut puts an account out for thirty minutes and arms its recheck at outUntil",
	"markOut keeps the later until, and a repeat answers unchanged",
	"a check with room before outUntil leaves the account out",
	"with every account out, pick answers no member and the soonest outUntil",
	"a due recheck hands the handler its event once, whose check with room opens the account",
	"a recheck whose check finds no room puts the account out until resetsAt and arms the recheck there",
	"an account whose Budget meter has no room counts as out until the provider's figure lapses",
	"markOut and reportRoom without accounts.pool are forbidden",
	"reportRoom of a key no row holds is not found",
	"the MCP tool account_pool_pick lists and runs",
}

// TestGeneratedPoolEndToEnd serves fixture-pool-api's generated router under
// bun over fixture-pool-db's generated repositories on Postgres, in a schema
// of its own at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL with the database's
// create.sql applied, drives the behavior operations, pick a command without
// a row among them, through the API's generated TypeScript SDK and its MCP
// endpoint, and runs the behavior worker's generated queues
// (accountRechecksQueues: Account's deadlines and events) in process under
// the runtime's runWorker with a Workers of the test's own, which asks a
// fake provider for each recheck event's account and reports its check
// through the event's transaction, a deadline made due with SQL
// (testdata/behavior_pool_runtime.test.ts): every case of poolCases must
// pass. It skips without the database, and without bun unless
// SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must run.
func TestGeneratedPoolEndToEnd(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated TypeScript server's and worker's Pool and its rechecks' handler against Postgres")
	}
	fixture := poolFixture(t)
	tree := materializeAPI(t, fixture)
	tree.typeCheck(t)

	// The SDK, beside the package, resolving its types package by name.
	names := naming.Default()
	tsOutput, err := tsgen.Generate(fixture.schema, tsgen.Options{
		SchemaName:         poolAPI,
		Dependencies:       fixture.deps,
		DependencyPackages: map[string]string{poolDB: names.NpmTypesPackage(poolDB)},
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
	sdkDir := filepath.Join(tree.root, "sdk", "typescript", poolAPI)
	if err := sdkgen.WriteSDKWithTools(sdkOutput, fixture.endpoints, sdkDir, fixedClock); err != nil {
		t.Fatalf("write the SDK: %v", err)
	}
	typesPackage := names.NpmTypesPackage(poolAPI)
	link(t, tree.typesDirs[typesPackage], filepath.Join(sdkDir, "node_modules", filepath.FromSlash(typesPackage)))

	ddl, err := sqlgen.Generate(fixture.deps[poolDB], sqlgen.Options{SchemaName: poolDB})
	if err != nil {
		t.Fatalf("generate %s's DDL: %v", poolDB, err)
	}
	ddlDir := filepath.Join(tree.root, "sql", poolDB)
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatalf("write %s's DDL: %v", poolDB, err)
	}
	createSQL := filepath.Join(ddlDir, "test-create.sql")
	pgtest.WriteCreateSQL(t, filepath.Join(ddlDir, "create.sql"), createSQL)

	testFile := tree.stage(t, "behavior_pool_runtime.test.ts")
	report := filepath.Join(tree.root, "behavior_pool.junit.xml")
	run := exec.Command(tree.bun, "test", testFile, "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = tree.apiDir
	run.Env = append(os.Environ(), "API_DIR="+tree.apiDir, "SDK_DIR="+sdkDir, "CREATE_SQL="+createSQL, "SUPERSCHEMATIC_RULES_TEST_DATABASE_URL="+dsn)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("behavior_pool_runtime.test.ts failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range poolCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(poolCases) {
		t.Errorf("bun ran %d cases, and poolCases names %d:\n%s", len(cases), len(poolCases), junit)
	}
	if testing.Verbose() {
		t.Logf("behavior_pool_runtime.test.ts:\n%s", out)
	}
}
