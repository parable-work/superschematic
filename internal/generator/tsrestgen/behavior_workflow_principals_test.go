package tsrestgen

import (
	"os"
	"os/exec"
	"path/filepath"
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

// principalsAPI is the API that serves the operations of
// fixture-workflow-principals-db's Question (D104) under QuestionActions,
// for any signed-in caller: its status Workflow, whose transitions name who
// makes them (by), with an overridePermission and repeat: "unchanged", and
// its review Workflow, which has no repeat; and QuestionAdmin's
// reassignQuestion, which takes askedOf, so the build warns of it.
const principalsAPI = "fixture-workflow-principals-api"

// principalsDB is fixture-workflow-principals-api's database, its one DB
// dependency.
const principalsDB = "fixture-workflow-principals-db"

// principalsFixture is fixture-workflow-principals-api with its database
// and apigen's endpoints for it, read with one registry, which declares the
// behaviors.
func principalsFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, principalsDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", principalsDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, principalsAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", principalsAPI, err)
	}
	deps := map[string]*ir.Schema{principalsDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     principalsAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: principalsAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, depsInfo: principalsDeps}
}

// principalsDeps is fixture-workflow-principals-api's Deps as the dispatch
// layer sets it: a pool of its database, which lists postgres first, and
// the repositories its TypeScript types export.
var principalsDeps = DepsInfo{
	Database:             principalsDB,
	Dialect:              "postgres",
	ORM:                  "FixtureWorkflowPrincipalsDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-workflow-principals-db-types",
}

// principalsCheck is the file tsc checks the package for
// fixture-workflow-principals-api against beside it: a transition's from
// is optional and one of the status's states, as its to is, and its result
// carries unchanged, optional, which only a repeat sets (D104).
const principalsCheck = `import type { FixtureWorkflowPrincipalsDbOrm } from '@schemas/fixture-workflow-principals-db-types/orm';
import { buildRouter, type Implementations } from './index';

declare const implementations: Implementations;
declare const orm: FixtureWorkflowPrincipalsDbOrm;
declare const key: Parameters<FixtureWorkflowPrincipalsDbOrm['question']['getOne']>[0];

export const router = buildRouter(implementations, { orm });
export const moved = orm.question.status.transition(key, { to: 'answered' });
export const read = orm.question.status.transition(key, { to: 'answered', from: 'open' });
// @ts-expect-error from is a state of the status.
export const lost = orm.question.status.transition(key, { to: 'answered', from: 'lost' });
export async function unchanged(): Promise<boolean | undefined> {
  return (await read).unchanged;
}
`

// TestGeneratedWorkflowPrincipalsAPICompiles type-checks the package for
// fixture-workflow-principals-api against its type packages,
// fixture-workflow-principals-db's repositories among them, the real
// runtime and Hono, with principalsCheck.
func TestGeneratedWorkflowPrincipalsAPICompiles(t *testing.T) {
	tree := materializeAPI(t, principalsFixture(t))
	if err := os.WriteFile(filepath.Join(tree.apiDir, "principals.check.ts"), []byte(principalsCheck), 0o644); err != nil {
		t.Fatal(err)
	}
	tree.typeCheck(t)
}

// principalsCases are the cases behavior_workflow_principals_runtime.test.ts
// runs, by name: D104's end-to-end cases, which the Go and Rust servers run
// too, in one wording.
var principalsCases = []string{
	"the subject the question is asked of answers it",
	"another caller is refused as not_principal",
	"the question's creator withdraws it, by createdBy",
	"a caller holding the override permission answers it",
	"answering again answers unchanged and leaves the row's version",
	"a repeat on the Workflow without repeat is refused as already_in_state",
	"a from that is not the row's state is refused as stale",
	"a from that is the row's state moves it, and its retry is stale with the state it moved to",
}

// TestGeneratedWorkflowPrincipalsEndToEnd serves
// fixture-workflow-principals-api's generated router under bun over
// fixture-workflow-principals-db's generated repositories on Postgres, in
// a schema of its own at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL with the
// database's create.sql applied, and drives it through the API's generated
// TypeScript SDK, each field's operations under it
// (testdata/behavior_workflow_principals_runtime.test.ts): every case of
// principalsCases must pass. It skips without the database, and without
// bun unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must run.
func TestGeneratedWorkflowPrincipalsEndToEnd(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated TypeScript server's Workflow by, overridePermission, repeat and from against Postgres")
	}
	fixture := principalsFixture(t)
	tree := materializeAPI(t, fixture)
	tree.typeCheck(t)

	// The SDK, beside the package, resolving its types package by name.
	names := naming.Default()
	tsOutput, err := tsgen.Generate(fixture.schema, tsgen.Options{
		SchemaName:         principalsAPI,
		Dependencies:       fixture.deps,
		DependencyPackages: map[string]string{principalsDB: names.NpmTypesPackage(principalsDB)},
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
	sdkDir := filepath.Join(tree.root, "sdk", "typescript", principalsAPI)
	if err := sdkgen.WriteSDKWithTools(sdkOutput, fixture.endpoints, sdkDir, fixedClock); err != nil {
		t.Fatalf("write the SDK: %v", err)
	}
	typesPackage := names.NpmTypesPackage(principalsAPI)
	link(t, tree.typesDirs[typesPackage], filepath.Join(sdkDir, "node_modules", filepath.FromSlash(typesPackage)))

	ddl, err := sqlgen.Generate(fixture.deps[principalsDB], sqlgen.Options{SchemaName: principalsDB})
	if err != nil {
		t.Fatalf("generate %s's DDL: %v", principalsDB, err)
	}
	ddlDir := filepath.Join(tree.root, "sql", principalsDB)
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatalf("write %s's DDL: %v", principalsDB, err)
	}
	createSQL := filepath.Join(ddlDir, "test-create.sql")
	pgtest.WriteCreateSQL(t, filepath.Join(ddlDir, "create.sql"), createSQL)

	testFile := tree.stage(t, "behavior_workflow_principals_runtime.test.ts")
	report := filepath.Join(tree.root, "behavior_workflow_principals.junit.xml")
	run := exec.Command(tree.bun, "test", testFile, "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = tree.apiDir
	run.Env = append(os.Environ(), "API_DIR="+tree.apiDir, "SDK_DIR="+sdkDir, "CREATE_SQL="+createSQL, "SUPERSCHEMATIC_RULES_TEST_DATABASE_URL="+dsn)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("behavior_workflow_principals_runtime.test.ts failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range principalsCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(principalsCases) {
		t.Errorf("bun ran %d cases, and principalsCases names %d:\n%s", len(cases), len(principalsCases), junit)
	}
	if testing.Verbose() {
		t.Logf("behavior_workflow_principals_runtime.test.ts:\n%s", out)
	}
}
