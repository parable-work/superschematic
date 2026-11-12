package tsrestgen

import (
	"maps"
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

// approvalsAPI is the API that serves the operations of
// fixture-approvals-db's Task (D104) under TaskActions, for any signed-in
// caller: its status Workflow's transition and the signOff,
// withdrawSignOff and listSignOffs of the Approvals it implements, beside
// Constants and StateRules, which add no operation; and TaskAdmin's
// reassignTask, which takes reviewer, a field StateRules names in by, so
// the build warns of it. The router serves its MCP tools.
const approvalsAPI = "fixture-approvals-api"

// approvalsDB is fixture-approvals-api's database, its one DB dependency.
const approvalsDB = "fixture-approvals-db"

// approvalsFixture is fixture-approvals-api with its database and apigen's
// endpoints for it, read with one registry, which declares the behaviors.
func approvalsFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, approvalsDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", approvalsDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, approvalsAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", approvalsAPI, err)
	}
	deps := map[string]*ir.Schema{approvalsDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     approvalsAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: approvalsAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, mcp: true, depsInfo: approvalsDeps}
}

// approvalsDeps is fixture-approvals-api's Deps as the dispatch layer sets
// it: a pool of its database, which lists postgres first, and the
// repositories its TypeScript types export.
var approvalsDeps = DepsInfo{
	Database:             approvalsDB,
	Dialect:              "postgres",
	ORM:                  "FixtureApprovalsDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-approvals-db-types",
}

// generateApprovalsFixture generates the package for fixture-approvals-api,
// its Deps set.
func generateApprovalsFixture(t *testing.T) *APIOutput {
	t.Helper()
	output := generateBehavior(t, approvalsFixture(t))
	if err := output.SetDeps(approvalsDeps); err != nil {
		t.Fatalf("SetDeps: %v", err)
	}
	return output
}

// TestWriteAPIGoldenBehaviorApprovals pins the package for
// fixture-approvals-api: its router mounts Approvals' operations under the
// behavior's member and the status's transition under its field, each run
// through RouterOptions.orm, and its implementation interface has
// reassignTask alone. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenBehaviorApprovals -update
func TestWriteAPIGoldenBehaviorApprovals(t *testing.T) {
	checkGolden(t, generateApprovalsFixture(t), approvalsAPI)
}

// TestGenerateApprovalsOperations: the operations of a use a table
// implements (D104) call the repository under the behavior's member,
// task.approvals.signOff, and a field use's under its field; Constants and
// StateRules add none. signOff's optional members, note and version, are
// the ones the route leaves out when null, so a null version reaches the
// core as none and a pinned gate refuses it as invalid_argument. The
// package has no behavior worker: Approvals arms no deadline and hears no
// write.
func TestGenerateApprovalsOperations(t *testing.T) {
	output := generateApprovalsFixture(t)
	if !output.TableBehaviors || !output.HasBehaviors() || output.HasBehaviorWorkers() {
		t.Errorf("TableBehaviors %t, HasBehaviors %t, HasBehaviorWorkers %t", output.TableBehaviors, output.HasBehaviors(), output.HasBehaviorWorkers())
	}
	routes := map[string]*BehaviorRoute{}
	for _, route := range output.BehaviorRoutes() {
		routes[route.Use+"."+route.Operation] = route
	}
	for use, method := range map[string]string{
		"Approvals.signOff":         "task.approvals.signOff",
		"Approvals.withdrawSignOff": "task.approvals.withdrawSignOff",
		"Approvals.listSignOffs":    "task.approvals.listSignOffs",
		"status.transition":         "task.status.transition",
	} {
		route := routes[use]
		if route == nil || route.Method() != method {
			t.Errorf("%s calls %+v, want %q", use, route, method)
		}
	}
	if len(routes) != 4 {
		t.Fatalf("behavior routes = %v, want Approvals' three and the status's transition", slices.Sorted(maps.Keys(routes)))
	}
	if got := routes["Approvals.signOff"].Optional; !slices.Equal(got, []string{"note", "version"}) {
		t.Errorf("signOff's optional members = %v, want [note version]", got)
	}
	if got := routes["Approvals.listSignOffs"].Optional; !slices.Equal(got, []string{"cursor", "limit", "state"}) {
		t.Errorf("listSignOffs' optional members = %v, want [cursor limit state]", got)
	}
}

// approvalsCheck is the file tsc checks the package for
// fixture-approvals-api against beside it: signOff's state is a gated
// state and its version optional, withdrawSignOff takes the gated state
// alone, and the ORM's withBehaviorCaller (D104) names who an update runs
// as, with the permissions Constants and StateRules check.
const approvalsCheck = `import type { FixtureApprovalsDbOrm } from '@schemas/fixture-approvals-db-types/orm';
import { buildRouter, type Implementations } from './index';

declare const implementations: Implementations;
declare const orm: FixtureApprovalsDbOrm;
declare const key: Parameters<FixtureApprovalsDbOrm['task']['getOne']>[0];

export const router = buildRouter(implementations, { orm });
export const signed = orm.task.approvals.signOff(key, { state: 'done', version: 3 });
export const unpinned = orm.task.approvals.signOff(key, { state: 'proving', note: 'Looks right' });
// @ts-expect-error a gate's state is one the config gates.
export const ungated = orm.task.approvals.signOff(key, { state: 'planned' });
export const withdrawn = orm.task.approvals.withdrawSignOff(key, { state: 'done' });
export const listed = orm.task.approvals.listSignOffs(key, { limit: 10 });
export const admin = orm.withBehaviorCaller({ subject: 'dave', permissions: ['tasks.admin'] }).task.updateOne(key, { urgent: false });
export async function signedBy(): Promise<string[]> {
  return (await signed).signedBy;
}
`

// TestGeneratedApprovalsAPICompiles type-checks the package for
// fixture-approvals-api against its type packages, fixture-approvals-db's
// repositories among them, the real runtime and Hono, with approvalsCheck.
func TestGeneratedApprovalsAPICompiles(t *testing.T) {
	tree := materializeAPI(t, approvalsFixture(t))
	if err := os.WriteFile(filepath.Join(tree.apiDir, "approvals.check.ts"), []byte(approvalsCheck), 0o644); err != nil {
		t.Fatal(err)
	}
	tree.typeCheck(t)
}

// approvalsCases are the cases behavior_approvals_runtime.test.ts runs, by
// name: D104's end-to-end cases of Constants, StateRules and Approvals,
// which the Go and Rust servers run too, in one wording, and the MCP
// endpoint's, which the TypeScript server's cases alone run.
var approvalsCases = []string{
	"a oneWay field moved back is refused as one_way, and moved forward is saved",
	"a caller holding tasks.admin moves a oneWay field back",
	"the creator edits the plan while the task is planned",
	"another caller editing the plan while the task is planned is refused as frozen_fields",
	"a move into proving without its sign-offs is refused as approvals_missing",
	"a pinned signOff with no version is refused as invalid_argument, with a stale one as stale, and with the row's version is saved",
	"editing the proof consumes the done gate's sign-offs and keeps the proving gate's",
	"an update of the owner, a constant, is refused as invalid_instance",
	"the MCP tool task_approvals_sign_off is listed with its schema, its version optional",
}

// TestGeneratedApprovalsEndToEnd serves fixture-approvals-api's generated
// router under bun over fixture-approvals-db's generated repositories on
// Postgres, in a schema of its own at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL
// with the database's create.sql applied, and drives the behavior
// operations through the API's generated TypeScript SDK and its MCP
// endpoint, and the creates and updates through the repositories, each
// update as a behavior caller (withBehaviorCaller)
// (testdata/behavior_approvals_runtime.test.ts): every case of
// approvalsCases must pass. It skips without the database, and without bun
// unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must run.
func TestGeneratedApprovalsEndToEnd(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated TypeScript server's Constants, StateRules and Approvals against Postgres")
	}
	fixture := approvalsFixture(t)
	tree := materializeAPI(t, fixture)
	tree.typeCheck(t)

	// The SDK, beside the package, resolving its types package by name.
	names := naming.Default()
	tsOutput, err := tsgen.Generate(fixture.schema, tsgen.Options{
		SchemaName:         approvalsAPI,
		Dependencies:       fixture.deps,
		DependencyPackages: map[string]string{approvalsDB: names.NpmTypesPackage(approvalsDB)},
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
	sdkDir := filepath.Join(tree.root, "sdk", "typescript", approvalsAPI)
	if err := sdkgen.WriteSDKWithTools(sdkOutput, fixture.endpoints, sdkDir, fixedClock); err != nil {
		t.Fatalf("write the SDK: %v", err)
	}
	typesPackage := names.NpmTypesPackage(approvalsAPI)
	link(t, tree.typesDirs[typesPackage], filepath.Join(sdkDir, "node_modules", filepath.FromSlash(typesPackage)))

	ddl, err := sqlgen.Generate(fixture.deps[approvalsDB], sqlgen.Options{SchemaName: approvalsDB})
	if err != nil {
		t.Fatalf("generate %s's DDL: %v", approvalsDB, err)
	}
	ddlDir := filepath.Join(tree.root, "sql", approvalsDB)
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatalf("write %s's DDL: %v", approvalsDB, err)
	}
	createSQL := filepath.Join(ddlDir, "test-create.sql")
	pgtest.WriteCreateSQL(t, filepath.Join(ddlDir, "create.sql"), createSQL)

	testFile := tree.stage(t, "behavior_approvals_runtime.test.ts")
	report := filepath.Join(tree.root, "behavior_approvals.junit.xml")
	run := exec.Command(tree.bun, "test", testFile, "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = tree.apiDir
	run.Env = append(os.Environ(), "API_DIR="+tree.apiDir, "SDK_DIR="+sdkDir, "CREATE_SQL="+createSQL, "SUPERSCHEMATIC_RULES_TEST_DATABASE_URL="+dsn)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("behavior_approvals_runtime.test.ts failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range approvalsCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(approvalsCases) {
		t.Errorf("bun ran %d cases, and approvalsCases names %d:\n%s", len(cases), len(approvalsCases), junit)
	}
	if testing.Verbose() {
		t.Logf("behavior_approvals_runtime.test.ts:\n%s", out)
	}
}
