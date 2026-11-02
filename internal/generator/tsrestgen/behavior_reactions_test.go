package tsrestgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// reactionsAPI is the API that serves fixture-reactions-db's Project and
// Task (D103): their statuses' operations under ProjectActions and
// TaskActions, and the behavior workers ProjectReactions and
// TaskReactions, each of which runs its table's change queue alone, since
// Reactions arms no deadline. Task's queue carries the writes Project's
// allTerminal and anyTerminal hear through task.project too.
const reactionsAPI = "fixture-reactions-api"

// reactionsDB is fixture-reactions-api's database, its one DB dependency.
const reactionsDB = "fixture-reactions-db"

// reactionsFixture is fixture-reactions-api with its database and apigen's
// endpoints for it, read with one registry, which declares the behaviors.
func reactionsFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, reactionsDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", reactionsDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, reactionsAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", reactionsAPI, err)
	}
	deps := map[string]*ir.Schema{reactionsDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     reactionsAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: reactionsAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, depsInfo: reactionsDeps}
}

// reactionsDeps is fixture-reactions-api's Deps as the dispatch layer sets
// it: a pool of its database, which lists postgres first, and the
// repositories its TypeScript types export.
var reactionsDeps = DepsInfo{
	Database:             reactionsDB,
	Dialect:              "postgres",
	ORM:                  "FixtureReactionsDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-reactions-db-types",
}

// generateReactionsFixture generates the package for fixture-reactions-api,
// its Deps set.
func generateReactionsFixture(t *testing.T) *APIOutput {
	t.Helper()
	output := generateBehavior(t, reactionsFixture(t))
	if err := output.SetDeps(reactionsDeps); err != nil {
		t.Fatalf("SetDeps: %v", err)
	}
	return output
}

// TestWriteAPIGoldenBehaviorReactions pins behavior-workers.ts for
// fixture-reactions-api: each worker's queues are its table's change queue
// alone, run through the repository's runChange. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenBehaviorReactions -update
func TestWriteAPIGoldenBehaviorReactions(t *testing.T) {
	output := generateReactionsFixture(t)
	dir := t.TempDir()
	if err := WriteAPI(output, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, BehaviorWorkersFile))
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "golden", reactionsAPI, BehaviorWorkersFile)
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if want, err := os.ReadFile(golden); err != nil || string(got) != string(want) {
		t.Errorf("%s differs from golden (run with -update to accept): %v", BehaviorWorkersFile, err)
	}
}

// TestGenerateChangeWorkers: a behavior worker of a table whose behaviors
// arm no deadlines and hear its writes runs its change queue alone (D103):
// TaskReactions runs TaskChange, which Task's Reactions hears and
// Project's hears through task.project, and builds no deadline queue.
func TestGenerateChangeWorkers(t *testing.T) {
	output := generateReactionsFixture(t)
	workers := output.BehaviorWorkers()
	if len(workers) != 2 {
		t.Fatalf("behavior workers = %+v", workers)
	}
	byName := map[string]WorkerInfo{}
	for _, w := range workers {
		byName[w.Name] = w
	}
	for name, want := range map[string]struct {
		table, queue, consts string
		heardBy              []string
	}{
		"ProjectReactions": {"Project", "ProjectChange", "projectChangeQueue", nil},
		"TaskReactions":    {"Task", "TaskChange", "taskChangeQueue", []string{"Project"}},
	} {
		w, ok := byName[name]
		if !ok {
			t.Fatalf("no behavior worker %s among %+v", name, workers)
		}
		b := w.Behavior
		if b.Table != want.table || !slices.Equal(b.Queues, []string{want.queue}) || b.ChangeQueue != want.queue || b.DeadlineQueue != "" {
			t.Errorf("%s = %+v", name, *b)
		}
		if len(b.Behaviors) != 0 || !slices.Equal(b.Hearing, []string{"Reactions"}) || !slices.Equal(b.HeardBy, want.heardBy) {
			t.Errorf("%s's behaviors: arming %v, hearing %v, heard by %v", name, b.Behaviors, b.Hearing, b.HeardBy)
		}
		if w.DeadlineQueueConst() != "" || w.ChangeQueueConst() != want.consts || w.QueueKinds() != "changes" {
			t.Errorf("%s's names: deadlines %q, changes %q, kinds %q", name, w.DeadlineQueueConst(), w.ChangeQueueConst(), w.QueueKinds())
		}
	}
	if consts := output.BehaviorQueueConsts(); !slices.Equal(consts, []string{"projectChangeQueue", "taskChangeQueue"}) {
		t.Errorf("behavior-workers.ts imports %v", consts)
	}
	// A worker that runs changes alone is refused without the repositories
	// as one that runs deadlines is, naming what it runs.
	bare := generateBehavior(t, reactionsFixture(t))
	var endpoints []EndpointInfo
	for _, ep := range bare.Endpoints {
		if ep.Behavior == nil {
			endpoints = append(endpoints, ep)
		}
	}
	bare.Endpoints = endpoints
	want := "tsrestgen: worker ProjectReactions runs Project's changes, through the repositories fixture-reactions-db's TypeScript types export for Postgres (D103); enable outputs.types.typescript in fixture-reactions-db's config"
	if err := bare.SetDeps(DepsInfo{Database: reactionsDB, Dialect: "postgres"}); err == nil || err.Error() != want {
		t.Errorf("SetDeps = %v,\nwant %s", err, want)
	}
}

// reactionsCheck is the file tsc checks the package for
// fixture-reactions-api against beside it: each worker's queues function
// builds its queues over a pool and the repositories.
const reactionsCheck = `import type { Pool } from 'pg';
import type { FixtureReactionsDbOrm } from '@schemas/fixture-reactions-db-types/orm';
import type { WorkerQueue } from '@superschematic/http-runtime/worker';
import { buildRouter, projectReactionsQueues, taskReactionsQueues, type Implementations } from './index';

declare const implementations: Implementations;
declare const orm: FixtureReactionsDbOrm;
declare const pool: Pool;

export const router = buildRouter(implementations, { orm });
export const queues: WorkerQueue[] = [...projectReactionsQueues(pool, orm), ...taskReactionsQueues(pool, orm)];
`

// TestGeneratedReactionsAPICompiles type-checks the package for
// fixture-reactions-api against its type packages, fixture-reactions-db's
// repositories (whose runChange the handlers call) and queues among them,
// the real runtime and Hono, with reactionsCheck.
func TestGeneratedReactionsAPICompiles(t *testing.T) {
	tree := materializeAPI(t, reactionsFixture(t))
	if err := os.WriteFile(filepath.Join(tree.apiDir, "reactions.check.ts"), []byte(reactionsCheck), 0o644); err != nil {
		t.Fatal(err)
	}
	tree.typeCheck(t)
}

// reactionsCases are the cases behavior_reactions_runtime.test.ts runs, by
// name: cases 1 to 5 of D103's end-to-end tests and a changes rule a raw
// write sets off, which the Go and Rust servers and workers run too.
var reactionsCases = []string{
	"a task that starts moves its project to active",
	"a project whose tasks are all done moves to done",
	"a task that fails moves its project to failed",
	"a raw SQL write of a task's status is heard and moves its project",
	"a raw SQL write of a task's priority to urgent starts it, which moves its project",
	"a then whose target has no transition to its state leaves the target as it is and completes the message",
}

// TestGeneratedReactionsEndToEnd serves fixture-reactions-api's generated
// router under bun over fixture-reactions-db's generated repositories on
// Postgres, in a schema of its own at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL
// with the database's create.sql applied (the tables' change queues and
// triggers among it), drives it through the API's generated TypeScript SDK
// and raw SQL, and runs both behavior workers' generated queues
// (projectReactionsQueues, taskReactionsQueues) in process under the
// runtime's runWorker (testdata/behavior_reactions_runtime.test.ts): every
// case of reactionsCases must pass. It skips without the database, and
// without bun unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must
// run.
func TestGeneratedReactionsEndToEnd(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated TypeScript server's and workers' Reactions against Postgres")
	}
	fixture := reactionsFixture(t)
	tree := materializeAPI(t, fixture)
	tree.typeCheck(t)

	// The SDK, beside the package, resolving its types package by name.
	names := naming.Default()
	tsOutput, err := tsgen.Generate(fixture.schema, tsgen.Options{
		SchemaName:         reactionsAPI,
		Dependencies:       fixture.deps,
		DependencyPackages: map[string]string{reactionsDB: names.NpmTypesPackage(reactionsDB)},
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
	sdkDir := filepath.Join(tree.root, "sdk", "typescript", reactionsAPI)
	if err := sdkgen.WriteSDKWithTools(sdkOutput, fixture.endpoints, sdkDir, fixedClock); err != nil {
		t.Fatalf("write the SDK: %v", err)
	}
	typesPackage := names.NpmTypesPackage(reactionsAPI)
	link(t, tree.typesDirs[typesPackage], filepath.Join(sdkDir, "node_modules", filepath.FromSlash(typesPackage)))

	ddl, err := sqlgen.Generate(fixture.deps[reactionsDB], sqlgen.Options{SchemaName: reactionsDB})
	if err != nil {
		t.Fatalf("generate %s's DDL: %v", reactionsDB, err)
	}
	ddlDir := filepath.Join(tree.root, "sql", reactionsDB)
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatalf("write %s's DDL: %v", reactionsDB, err)
	}
	createSQL := filepath.Join(ddlDir, "test-create.sql")
	pgtest.WriteCreateSQL(t, filepath.Join(ddlDir, "create.sql"), createSQL)

	testFile := tree.stage(t, "behavior_reactions_runtime.test.ts")
	report := filepath.Join(tree.root, "behavior_reactions.junit.xml")
	run := exec.Command(tree.bun, "test", testFile, "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = tree.apiDir
	run.Env = append(os.Environ(), "API_DIR="+tree.apiDir, "SDK_DIR="+sdkDir, "CREATE_SQL="+createSQL, "SUPERSCHEMATIC_RULES_TEST_DATABASE_URL="+dsn)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("behavior_reactions_runtime.test.ts failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range reactionsCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(reactionsCases) {
		t.Errorf("bun ran %d cases, and reactionsCases names %d:\n%s", len(cases), len(reactionsCases), junit)
	}
	if testing.Verbose() {
		t.Logf("behavior_reactions_runtime.test.ts:\n%s", out)
	}
}
