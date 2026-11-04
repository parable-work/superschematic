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

// agentsAPI is the API that serves fixture-agents-db's tables (D104) for
// any signed-in caller: JobActions, the operations of the Lease and Retries
// Job implements and of its status's Workflow, each of which takes the
// caller's preconditions (Lease's fencing token); SessionActions, Session's
// Presence and status; AccountActions, Account's Budget and status; and
// the behavior workers JobDeadlines, which runs Job's deadlines (a lease's
// expiry, the end of a backoff), and SessionPresence, which runs each
// session's miss holding jobs.override, the permission a miss's expiry of
// the device's leases needs. The router serves its MCP tools.
const agentsAPI = "fixture-agents-api"

// agentsDB is fixture-agents-api's database, its one DB dependency.
const agentsDB = "fixture-agents-db"

// agentsFixture is fixture-agents-api with its database and apigen's
// endpoints for it, read with one registry, which declares the behaviors.
func agentsFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, agentsDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", agentsDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, agentsAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", agentsAPI, err)
	}
	deps := map[string]*ir.Schema{agentsDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     agentsAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: agentsAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, mcp: true, depsInfo: agentsDeps}
}

// agentsDeps is fixture-agents-api's Deps as the dispatch layer sets it: a
// pool of its database, which lists postgres first, and the repositories
// its TypeScript types export.
var agentsDeps = DepsInfo{
	Database:             agentsDB,
	Dialect:              "postgres",
	ORM:                  "FixtureAgentsDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-agents-db-types",
}

// generateAgentsFixture generates the package for fixture-agents-api, its
// Deps set.
func generateAgentsFixture(t *testing.T) *APIOutput {
	t.Helper()
	output := generateBehavior(t, agentsFixture(t))
	if err := output.SetDeps(agentsDeps); err != nil {
		t.Fatalf("SetDeps: %v", err)
	}
	return output
}

// TestWriteAPIGoldenBehaviorAgents pins the package for fixture-agents-api:
// its router mounts Job's operations under the behaviors' members, each
// handing the repository its parameters without the input's preconditions
// and presenting them in the operation's options, Session's and Account's
// beside them, and behavior-workers.ts exports JobDeadlines' and
// SessionPresence's queues. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenBehaviorAgents -update
func TestWriteAPIGoldenBehaviorAgents(t *testing.T) {
	output := generateAgentsFixture(t)
	checkGolden(t, output, agentsAPI)
	dir := t.TempDir()
	if err := WriteAPI(output, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, BehaviorWorkersFile))
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "golden", agentsAPI, BehaviorWorkersFile)
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

// TestGenerateAgentsOperations: every behavior operation of a table one of
// whose uses takes a precondition (Job's Lease, D104) takes the input's
// preconditions apart from its parameters, an operation with no parameters
// among them, and leaves them out of the optional members it hands the
// repository; a table whose uses take none (Session, Account) has no such
// member. Each operation calls the repository under its use's member. The
// package has two behavior workers, each over its table's deadline queue,
// SessionPresence's caller holding the override its miss needs.
func TestGenerateAgentsOperations(t *testing.T) {
	output := generateAgentsFixture(t)
	if !output.TableBehaviors || !output.HasBehaviors() || !output.HasBehaviorWorkers() || !output.BehaviorsTakePreconditions() {
		t.Errorf("TableBehaviors %t, HasBehaviors %t, HasBehaviorWorkers %t, BehaviorsTakePreconditions %t", output.TableBehaviors, output.HasBehaviors(), output.HasBehaviorWorkers(), output.BehaviorsTakePreconditions())
	}
	routes := map[string]*BehaviorRoute{}
	for _, route := range output.BehaviorRoutes() {
		routes[route.Type+"."+route.Use+"."+route.Operation] = route
	}
	for name, method := range map[string]string{
		"Job.Lease.acquire":           "job.lease.acquire",
		"Job.Lease.heartbeat":         "job.lease.heartbeat",
		"Job.Lease.release":           "job.lease.release",
		"Job.Lease.expire":            "job.lease.expire",
		"Job.Lease.direct":            "job.lease.direct",
		"Job.Lease.acknowledge":       "job.lease.acknowledge",
		"Job.Lease.resetExpiries":     "job.lease.resetExpiries",
		"Job.Retries.recordAttempt":   "job.retries.recordAttempt",
		"Job.status.transition":       "job.status.transition",
		"Session.Presence.beat":       "session.presence.beat",
		"Session.status.transition":   "session.status.transition",
		"Account.Budget.recordUsage":  "account.budget.recordUsage",
		"Account.Budget.observe":      "account.budget.observe",
		"Account.status.transition":   "account.status.transition",
		"Account.Budget.checkReserve": "account.budget.checkReserve",
	} {
		route := routes[name]
		if route == nil || route.Method() != method {
			t.Errorf("%s calls %+v, want %q", name, route, method)
			continue
		}
		if want := route.Type == "Job"; route.Preconditions != want {
			t.Errorf("%s takes preconditions %t, want %t", name, route.Preconditions, want)
		}
		if slices.Contains(route.Optional, PreconditionsMember) {
			t.Errorf("%s hands the repository its preconditions among its optional parameters %v", name, route.Optional)
		}
	}
	if _, ok := routes["Job.Lease.expireHolder"]; ok {
		t.Errorf("expireHolder runs on the table as a whole, and the set serves it: %v", routes["Job.Lease.expireHolder"])
	}
	if got := routes["Job.Lease.heartbeat"].Optional; !slices.Equal(got, []string{"acknowledge"}) {
		t.Errorf("heartbeat's optional members = %v, want [acknowledge]", got)
	}
	if got := routes["Job.Lease.resetExpiries"].Optional; len(got) != 0 {
		t.Errorf("resetExpiries' optional members = %v, want none", got)
	}

	workers := output.BehaviorWorkers()
	names := map[string]apigen.BehaviorWorkerInfo{}
	for _, w := range workers {
		names[w.Name] = *w.Behavior
	}
	job, session := names["JobDeadlines"], names["SessionPresence"]
	if len(workers) != 2 || job.Table != "Job" || job.DeadlineQueue != "JobDeadline" || len(job.Permissions) != 0 {
		t.Errorf("JobDeadlines = %+v (workers %d)", job, len(workers))
	}
	if session.Table != "Session" || session.DeadlineQueue != "SessionDeadline" || !slices.Equal(session.Permissions, []string{"jobs.override"}) {
		t.Errorf("SessionPresence = %+v", session)
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
		"behaviorParams(request.input, ['acknowledge'], ['preconditions']) as never",
		"behaviorParams(request.input, [], ['preconditions']) as never",
		"{ caller: behaviorCaller(ctx), preconditions: behaviorPreconditions(request.input) as never }",
		"behaviorPreconditions,",
	} {
		if !strings.Contains(string(router), want) {
			t.Errorf("router.ts has no %s", want)
		}
	}
}

// agentsCheck is the file tsc checks the package for fixture-agents-api
// against beside it: the router takes the repositories, each worker's
// queues are built over a pool and them, and the repositories type Job's
// preconditions (JobPreconditions) on its operations, updateOne and
// deleteOne, and refuse an entry of another type.
const agentsCheck = `import type { Pool } from 'pg';
import type { FixtureAgentsDbOrm, JobPreconditions } from '@schemas/fixture-agents-db-types/orm';
import type { WorkerQueue } from '@superschematic/http-runtime/worker';
import { buildRouter, jobDeadlinesQueues, sessionPresenceQueues, type Implementations } from './index';

declare const implementations: Implementations;
declare const orm: FixtureAgentsDbOrm;
declare const pool: Pool;
declare const key: Parameters<FixtureAgentsDbOrm['job']['getOne']>[0];

export const router = buildRouter(implementations, { orm });
export const queues: WorkerQueue[] = [...jobDeadlinesQueues(pool, orm), ...sessionPresenceQueues(pool, orm)];
export const fenced: JobPreconditions = { Lease: { token: 3 } };
export const beat = orm.job.lease.heartbeat(key, {}, { preconditions: fenced });
export const moved = orm.job.status.transition(key, { to: 'running' }, { preconditions: { Lease: { token: 1 } } });
export const renamed = orm.job.updateOne(key, { title: 'Build' }, { preconditions: { Lease: { token: 1 } } });
export const deleted = orm.job.deleteOne(key, { preconditions: { Lease: { token: 1 } } });
// @ts-expect-error a token is a number.
export const wrong = orm.job.updateOne(key, { title: 'Build' }, { preconditions: { Lease: { token: '1' } } });
export const recorded = orm.job.retries.recordAttempt(key, { failure: 'timeout' }, { preconditions: fenced });
`

// TestGeneratedAgentsAPICompiles type-checks the package for
// fixture-agents-api against its type packages, fixture-agents-db's
// repositories and queues among them, the real runtime and Hono, with
// agentsCheck.
func TestGeneratedAgentsAPICompiles(t *testing.T) {
	tree := materializeAPI(t, agentsFixture(t))
	if err := os.WriteFile(filepath.Join(tree.apiDir, "agents.check.ts"), []byte(agentsCheck), 0o644); err != nil {
		t.Fatal(err)
	}
	tree.typeCheck(t)
}

// agentsCases are the cases behavior_agents_runtime.test.ts runs, by name:
// D104's end-to-end cases of Lease, Retries, Presence and Budget on a
// table, which the Go and Rust servers and workers run too, in one wording.
// The TypeScript server's MCP endpoint is held within the cases of the
// heartbeat and of observe.
var agentsCases = []string{
	"a worker acquires a job's lease and gets its fencing token, and a heartbeat that presents it renews the lease",
	"under requireToken a write that presents no token is refused as token_required, and the holder's write that presents it is saved",
	"a token a later lease replaced is refused as token_stale",
	"another caller's acquire of a job whose lease is active is refused as held_by_another",
	"a failure in a class with a backoff sets retryAt, and until it passes the move into running and the lease's acquire are refused as backing_off",
	"when the backoff ends its then, run by the worker, moves the waiting job back to queued",
	"a lease that lapses expires when its deadline falls due on the worker: onExpiry moves the job back to queued and the token advances",
	"a second build failure of one revision exhausts the job, and a new revision starts its counts again",
	"a precondition for a use whose behavior takes none is refused as invalid_argument",
	"a session's create arms its miss ttlMs on: its deadline and its queue message",
	"only the device's principal beats a session: another caller is 409 not_principal, and the device's beat moves the deadline ttlMs on",
	"a session's transition into turn arms its miss fifteen minutes from the entry (ttlByState)",
	"a due miss the worker runs moves the session to missing and expires the leases its device holds on jobs (releaseLeases)",
	"a beat recovers a missed session: missed clears and onBeat moves it back to idle",
	"an update that changes a session's device is 409 principal_fixed",
	"recordUsage charges the account's hourly meter and its workspace's through the to-one relation",
	"a reservation past the account's limit is 409 over_limit, and leaves the rows as they were",
	"an hourly meter starts again at the next hour: its next write counts from zero",
	"a rolling meter counts the last day's buckets: usage that left the window no longer counts",
	"observe without observePermission is 403 forbidden, and with it the provider's figure replaces what the meter counts until resetsAt",
	"usage that takes the account over its limit moves it out (escalate), and counts all the same",
}

// TestGeneratedAgentsEndToEnd serves fixture-agents-api's generated router
// under bun over fixture-agents-db's generated repositories on Postgres, in
// a schema of its own at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL with the
// database's create.sql applied, drives the behavior operations through the
// API's generated TypeScript SDK and its MCP endpoint, the creates and
// updates through the repositories as a behavior caller
// (withBehaviorCaller), a write under a lease presenting its token in the
// body's preconditions and in updateOne's options, and runs the behavior
// workers' generated queues (jobDeadlinesQueues, sessionPresenceQueues) in
// process under the runtime's runWorker, each deadline made due with SQL
// (testdata/behavior_agents_runtime.test.ts): every case of agentsCases
// must pass. It skips without the database, and without bun unless
// SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must run.
func TestGeneratedAgentsEndToEnd(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated TypeScript server's and workers' Lease, Retries, Presence and Budget against Postgres")
	}
	fixture := agentsFixture(t)
	tree := materializeAPI(t, fixture)
	tree.typeCheck(t)

	// The SDK, beside the package, resolving its types package by name.
	names := naming.Default()
	tsOutput, err := tsgen.Generate(fixture.schema, tsgen.Options{
		SchemaName:         agentsAPI,
		Dependencies:       fixture.deps,
		DependencyPackages: map[string]string{agentsDB: names.NpmTypesPackage(agentsDB)},
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
	sdkDir := filepath.Join(tree.root, "sdk", "typescript", agentsAPI)
	if err := sdkgen.WriteSDKWithTools(sdkOutput, fixture.endpoints, sdkDir, fixedClock); err != nil {
		t.Fatalf("write the SDK: %v", err)
	}
	typesPackage := names.NpmTypesPackage(agentsAPI)
	link(t, tree.typesDirs[typesPackage], filepath.Join(sdkDir, "node_modules", filepath.FromSlash(typesPackage)))

	ddl, err := sqlgen.Generate(fixture.deps[agentsDB], sqlgen.Options{SchemaName: agentsDB})
	if err != nil {
		t.Fatalf("generate %s's DDL: %v", agentsDB, err)
	}
	ddlDir := filepath.Join(tree.root, "sql", agentsDB)
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatalf("write %s's DDL: %v", agentsDB, err)
	}
	createSQL := filepath.Join(ddlDir, "test-create.sql")
	pgtest.WriteCreateSQL(t, filepath.Join(ddlDir, "create.sql"), createSQL)

	testFile := tree.stage(t, "behavior_agents_runtime.test.ts")
	report := filepath.Join(tree.root, "behavior_agents.junit.xml")
	run := exec.Command(tree.bun, "test", testFile, "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = tree.apiDir
	run.Env = append(os.Environ(), "API_DIR="+tree.apiDir, "SDK_DIR="+sdkDir, "CREATE_SQL="+createSQL, "SUPERSCHEMATIC_RULES_TEST_DATABASE_URL="+dsn)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("behavior_agents_runtime.test.ts failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range agentsCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(agentsCases) {
		t.Errorf("bun ran %d cases, and agentsCases names %d:\n%s", len(cases), len(agentsCases), junit)
	}
	if testing.Verbose() {
		t.Logf("behavior_agents_runtime.test.ts:\n%s", out)
	}
}
