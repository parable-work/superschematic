package tsrestgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// startAPI is the API whose create names the state its row starts in
// (D109): OrderViews.placeOrder, a @tableOperation over fixture-workflow-db's
// Order, whose body holds status, the field of Order's Workflow.
const startAPI = "fixture-workflow-view-api"

// startFixture is fixture-workflow-view-api with its database and apigen's
// endpoints for it, read with one registry, which declares the behaviors.
func startFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, behaviorDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", behaviorDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, startAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", startAPI, err)
	}
	deps := map[string]*ir.Schema{behaviorDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     startAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: startAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, depsInfo: behaviorDeps}
}

// TestGenerateACreateThatNamesItsStartingState: the router's table holds
// placeOrder's start, status, beside its body's members, and the loader
// recorded it.
func TestGenerateACreateThatNamesItsStartingState(t *testing.T) {
	fixture := startFixture(t)
	output, err := Generate(fixture.schema, fixture.endpoints, Options{SchemaName: fixture.name, Dependencies: fixture.deps, Clock: fixedClock})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var place *TableRoute
	for _, ep := range output.Endpoints {
		if ep.Name == "placeOrder" {
			place = ep.Table
		}
	}
	if place == nil {
		t.Fatal("placeOrder is no table operation of the router")
	}
	const want = "{ kind: 'create', type: 'Order', repository: 'order', view: ['id', 'title', 'status', 'statusEnteredAt'], body: ['title', 'status'], starts: ['status'] }"
	if got := place.Literal(); got != want {
		t.Errorf("placeOrder's entry =\n%s\nwant\n%s", got, want)
	}
}

// startCases are the cases start_state_runtime.test.ts runs, by name.
var startCases = []string{
	"a create that names no state leaves the row in the Workflow's initial state",
	"a create that names a state moves the row there by the Workflow's transition, in the create's transaction",
	"a state the initial one does not lead to is the transition's refusal, and no row is kept",
	"a state the Workflow does not have is refused, and no row is kept",
	"the route's guard holds",
}

// TestGeneratedStartStateCreateEndToEnd serves fixture-workflow-view-api's
// generated router under bun over fixture-workflow-db's generated
// repositories and the behaviors' core on Postgres, in a schema of its
// own at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL, and places orders over
// HTTP (testdata/start_state_runtime.test.ts): a create whose body names
// the state its row starts in (D109). Every case of startCases must pass.
// It skips without the database.
func TestGeneratedStartStateCreateEndToEnd(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run a create that names its starting state against Postgres")
	}
	fixture := startFixture(t)
	tree := materializeAPI(t, fixture)
	tree.typeCheck(t)

	ddl, err := sqlgen.Generate(fixture.deps[behaviorDB], sqlgen.Options{SchemaName: behaviorDB})
	if err != nil {
		t.Fatalf("generate %s's DDL: %v", behaviorDB, err)
	}
	ddlDir := filepath.Join(tree.root, "sql", behaviorDB)
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatalf("write %s's DDL: %v", behaviorDB, err)
	}
	createSQL := filepath.Join(ddlDir, "test-create.sql")
	pgtest.WriteCreateSQL(t, filepath.Join(ddlDir, "create.sql"), createSQL)

	testFile := tree.stage(t, "start_state_runtime.test.ts")
	report := filepath.Join(tree.root, "start_state.junit.xml")
	run := exec.Command(tree.bun, "test", testFile, "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = tree.apiDir
	run.Env = append(os.Environ(), "API_DIR="+tree.apiDir, "CREATE_SQL="+createSQL, "SUPERSCHEMATIC_RULES_TEST_DATABASE_URL="+dsn)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("start_state_runtime.test.ts failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range startCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(startCases) {
		t.Errorf("bun ran %d cases, and startCases names %d:\n%s", len(cases), len(startCases), junit)
	}
	if testing.Verbose() {
		t.Logf("start_state_runtime.test.ts:\n%s", out)
	}
}
