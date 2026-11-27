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
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	ir "github.com/parable-work/superschematic/ir"
)

// recurrenceAPI is the API that serves fixture-recurrence-db's Filing
// (D152) for any signed-in caller: FilingActions, the setRecurrence,
// complete, skip, moveOne and listOccurrences of the Recurrence Filing
// implements; and the behavior worker FilingDeadlines, which fires each
// filing's open, remind, overdue and plan deadlines and plans again on a
// write its Recurrence hears.
const recurrenceAPI = "fixture-recurrence-api"

// recurrenceDB is fixture-recurrence-api's database, its one DB dependency.
const recurrenceDB = "fixture-recurrence-db"

// recurrenceFixture is fixture-recurrence-api with its database and
// apigen's endpoints for it, read with one registry.
func recurrenceFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, recurrenceDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", recurrenceDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, recurrenceAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", recurrenceAPI, err)
	}
	deps := map[string]*ir.Schema{recurrenceDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     recurrenceAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: recurrenceAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, mcp: true, depsInfo: recurrenceDeps}
}

// recurrenceDeps is fixture-recurrence-api's Deps as the dispatch layer
// sets it.
var recurrenceDeps = DepsInfo{
	Database:             recurrenceDB,
	Dialect:              "postgres",
	ORM:                  "FixtureRecurrenceDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-recurrence-db-types",
}

func generateRecurrenceFixture(t *testing.T) *APIOutput {
	t.Helper()
	output := generateBehavior(t, recurrenceFixture(t))
	if err := output.SetDeps(recurrenceDeps); err != nil {
		t.Fatalf("SetDeps: %v", err)
	}
	return output
}

// TestWriteAPIGoldenBehaviorRecurrence pins the package for
// fixture-recurrence-api: its router mounts Filing's Recurrence operations
// under recurrence, and behavior-workers.ts exports FilingDeadlines'
// queues, its deadlines, changes and events, which Recurrence emits as it
// opens, reminds and misses an occurrence (D153). Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenBehaviorRecurrence -update
func TestWriteAPIGoldenBehaviorRecurrence(t *testing.T) {
	output := generateRecurrenceFixture(t)
	checkGolden(t, output, recurrenceAPI)
	dir := t.TempDir()
	if err := WriteAPI(output, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, BehaviorWorkersFile))
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "golden", recurrenceAPI, BehaviorWorkersFile)
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

// TestGenerateRecurrenceOperations: each Recurrence operation of
// fixture-recurrence-db's Filing calls the repository under its use's
// member, recurrence; the one behavior worker runs Filing's deadline,
// change and event queues, and hands each event Recurrence emits (opened,
// reminded, missed, D153) to a Workers method.
func TestGenerateRecurrenceOperations(t *testing.T) {
	output := generateRecurrenceFixture(t)
	if !output.TableBehaviors || !output.HasBehaviors() || !output.HasBehaviorWorkers() || output.BehaviorsTakePreconditions() {
		t.Errorf("TableBehaviors %t, HasBehaviors %t, HasBehaviorWorkers %t, BehaviorsTakePreconditions %t", output.TableBehaviors, output.HasBehaviors(), output.HasBehaviorWorkers(), output.BehaviorsTakePreconditions())
	}
	routes := map[string]*BehaviorRoute{}
	for _, route := range output.BehaviorRoutes() {
		routes[route.Type+"."+route.Use+"."+route.Operation] = route
	}
	for _, name := range []string{"setRecurrence", "complete", "skip", "moveOne", "listOccurrences"} {
		route := routes["Filing.Recurrence."+name]
		if route == nil || route.Method() != "filing.recurrence."+name {
			t.Errorf("Filing.Recurrence.%s calls %+v, want filing.recurrence.%s", name, route, name)
		}
	}
	if len(routes) != 5 {
		t.Errorf("the router mounts %d behavior operations, want 5: %v", len(routes), slices.Sorted(maps.Keys(routes)))
	}
	workers := output.BehaviorWorkers()
	if len(workers) != 1 {
		t.Fatalf("behavior workers = %d, want FilingDeadlines alone", len(workers))
	}
	w := workers[0]
	if w.Name != "FilingDeadlines" || w.Behavior.Table != "Filing" || w.Behavior.DeadlineQueue != "FilingDeadline" || w.Behavior.ChangeQueue != "FilingChange" || w.Behavior.EventQueue != "FilingEvent" || !w.Emits() || len(w.Behavior.Events) != 3 {
		t.Errorf("FilingDeadlines = %s %+v", w.Name, *w.Behavior)
	}
	dir := t.TempDir()
	if err := WriteAPI(output, dir); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(filepath.Join(dir, "router.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"orm.filing.recurrence.setRecurrence(", "orm.filing.recurrence.complete(", "orm.filing.recurrence.listOccurrences("} {
		if !strings.Contains(string(text), want) {
			t.Errorf("router.ts has no %s", want)
		}
	}
}

// recurrenceCheck is the file tsc checks the package for
// fixture-recurrence-api against beside it: the router takes the
// repositories, the worker's queues are built over a pool and them, and
// each operation of the repository is typed by its declaration.
const recurrenceCheck = `import type { Pool } from 'pg';
import type { FixtureRecurrenceDbOrm } from '@schemas/fixture-recurrence-db-types/orm';
import type { WorkerQueue } from '@superschematic/http-runtime/worker';
import { buildRouter, filingDeadlinesQueues, type Implementations } from './index';
import type { Workers } from './deps';

declare const implementations: Implementations;
declare const orm: FixtureRecurrenceDbOrm;
declare const pool: Pool;
declare const workers: Workers;
declare const filing: Parameters<FixtureRecurrenceDbOrm['filing']['getOne']>[0];

export const router = buildRouter(implementations, { orm });
export const queues: WorkerQueue[] = filingDeadlinesQueues(pool, orm, workers);
export const set = orm.filing.recurrence.setRecurrence(filing, { anchor: 'calendar', rule: 'FREQ=YEARLY', start: '2026-10-31T09:00' });
export const done = orm.filing.recurrence.complete(filing, { completedBy: { table: 'Payment', key: 'p1' } });
export const listed = orm.filing.recurrence.listOccurrences(filing, { from: 0, to: 1 });
// @ts-expect-error weekly is no anchor.
export const weekly = orm.filing.recurrence.setRecurrence(filing, { anchor: 'weekly' });
`

// recurrenceCases are the cases behavior_recurrence_runtime.test.ts runs,
// by name: D152's end-to-end cases of Recurrence on a table.
var recurrenceCases = []string{
	"setRecurrence plans the occurrences up to the horizon, moved off the row calendar holidays, and arms the open, remind and overdue deadlines",
	"a rule change leaves the open occurrence untouched and replans the ones that have not opened",
	"the remind deadline fires on the worker, records the reminder and emits the opening and the reminder",
	"complete records what completed an occurrence and who, and an afterDone recurrence dates its next from it",
	"without filings.manage setRecurrence is 403 and a rule that is no RRULE is 400",
	"the calendar merges a contract end date and a filing occurrences, each dated as its source says",
	"a raw SQL write of a filing holiday calendar is heard by the worker, which plans again",
}

// TestGeneratedRecurrenceEndToEnd serves fixture-recurrence-api's
// generated router under bun over fixture-recurrence-db's generated
// repositories on Postgres, in a schema of its own at
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL with the database's create.sql
// applied, and runs the behavior worker's generated queues in process
// (testdata/behavior_recurrence_runtime.test.ts): every case of
// recurrenceCases must pass. It skips without the database.
func TestGeneratedRecurrenceEndToEnd(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated TypeScript server's and worker's Recurrence against Postgres")
	}
	fixture := recurrenceFixture(t)
	tree := materializeAPI(t, fixture)
	tree.typeCheck(t)
	ddl, err := sqlgen.Generate(fixture.deps[recurrenceDB], sqlgen.Options{SchemaName: recurrenceDB})
	if err != nil {
		t.Fatalf("generate %s's DDL: %v", recurrenceDB, err)
	}
	ddlDir := filepath.Join(tree.root, "sql", recurrenceDB)
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatalf("write %s's DDL: %v", recurrenceDB, err)
	}
	createSQL := filepath.Join(ddlDir, "test-create.sql")
	pgtest.WriteCreateSQL(t, filepath.Join(ddlDir, "create.sql"), createSQL)

	testFile := tree.stage(t, "behavior_recurrence_runtime.test.ts")
	report := filepath.Join(tree.root, "behavior_recurrence.junit.xml")
	run := exec.Command(tree.bun, "test", testFile, "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = tree.apiDir
	run.Env = append(os.Environ(), "API_DIR="+tree.apiDir, "CREATE_SQL="+createSQL, "SUPERSCHEMATIC_RULES_TEST_DATABASE_URL="+dsn)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("behavior_recurrence_runtime.test.ts failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range recurrenceCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(recurrenceCases) {
		t.Errorf("bun ran %d cases, and recurrenceCases names %d:\n%s", len(cases), len(recurrenceCases), junit)
	}
	if testing.Verbose() {
		t.Logf("behavior_recurrence_runtime.test.ts:\n%s", out)
	}
}

// TestGeneratedRecurrenceEndToEndOnSQLite serves fixture-recurrence-api's
// generated router under bun over fixture-recurrence-db's generated
// repositories on SQLite (D186): the ./orm/sqlite entry's createOrm on
// openSqlite's connection to a file its sqlite/create.sql made, whose
// behaviors run through the core in its transactions, with the behavior
// worker's handlers over the SQLite queue client. It runs
// behavior_recurrence_runtime.test.ts with ORM_DIALECT sqlite, every case
// of recurrenceCases, which needs no database server.
func TestGeneratedRecurrenceEndToEndOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated TypeScript server's and worker's Recurrence on SQLite in -short mode")
	}
	fixture := recurrenceFixture(t)
	fixture.sqlite = true
	tree := materializeAPI(t, fixture)
	tree.typeCheck(t)
	model, err := sqlmigrate.BuildModel(fixture.deps[recurrenceDB], sqlgen.Options{SchemaName: recurrenceDB}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("build %s's SQLite model: %v", recurrenceDB, err)
	}
	script, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatalf("write %s's SQLite create.sql: %v", recurrenceDB, err)
	}
	createSQL := filepath.Join(tree.root, "sql", recurrenceDB, "sqlite", "create.sql")
	if err := os.MkdirAll(filepath.Dir(createSQL), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(createSQL, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	testFile := tree.stage(t, "behavior_recurrence_runtime.test.ts")
	report := filepath.Join(tree.root, "behavior_recurrence-sqlite.junit.xml")
	run := exec.Command(tree.bun, "test", testFile, "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = tree.apiDir
	run.Env = append(os.Environ(), "API_DIR="+tree.apiDir, "CREATE_SQL="+createSQL, "ORM_DIALECT=sqlite", "SQLITE_FILE="+filepath.Join(tree.root, "recurrence.sqlite"))
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("behavior_recurrence_runtime.test.ts on SQLite failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range recurrenceCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass on SQLite:\n%s", name, junit)
		}
	}
	if len(cases) != len(recurrenceCases) {
		t.Errorf("bun ran %d cases, and recurrenceCases names %d:\n%s", len(cases), len(recurrenceCases), junit)
	}
	if testing.Verbose() {
		t.Logf("behavior_recurrence_runtime.test.ts on SQLite:\n%s", out)
	}
}

// TestGeneratedRecurrenceAPICompiles type-checks the package for
// fixture-recurrence-api against its type packages, the real runtime and
// Hono, with recurrenceCheck.
func TestGeneratedRecurrenceAPICompiles(t *testing.T) {
	tree := materializeAPI(t, recurrenceFixture(t))
	if err := os.WriteFile(filepath.Join(tree.apiDir, "recurrence.check.ts"), []byte(recurrenceCheck), 0o644); err != nil {
		t.Fatal(err)
	}
	tree.typeCheck(t)
}
