package tsrestgen

import (
	"os"
	"os/exec"
	"path/filepath"
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

// accessAPI is the API whose operations the build serves from the tables
// of fixture-access-db (D156): folders and documents that hold an
// audience, contracts whose requests a row rule reaches through three
// relations, the grants that let a party reach rows, and the access log.
const accessAPI = "fixture-access-api"

// accessDB is fixture-access-api's database, its one DB dependency.
const accessDB = "fixture-access-db"

// accessFixture is fixture-access-api with its database and apigen's
// endpoints for it.
func accessFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, accessDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", accessDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, accessAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", accessAPI, err)
	}
	deps := map[string]*ir.Schema{accessDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     accessAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: accessAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, depsInfo: accessDeps}
}

// accessDeps is fixture-access-api's Deps as the dispatch layer sets it.
var accessDeps = DepsInfo{
	Database:             accessDB,
	Dialect:              "postgres",
	ORM:                  "FixtureAccessDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-access-db-types",
}

// TestAccessRoutesCarryTheirRulesAndReveals: the router's table of
// operations holds a row rule's path as written, and a reveal's
// permission by member (D156).
func TestAccessRoutesCarryTheirRulesAndReveals(t *testing.T) {
	fixture := accessFixture(t)
	output, err := Generate(fixture.schema, fixture.endpoints, Options{SchemaName: fixture.name, Dependencies: fixture.deps, Clock: fixedClock})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := output.SetDeps(accessDeps); err != nil {
		t.Fatalf("SetDeps: %v", err)
	}
	router := ""
	for _, ep := range output.Endpoints {
		if ep.Table != nil {
			router += ep.Table.Literal() + "\n"
		}
	}
	for _, want := range []string{
		"by: ['unit.contract.parties.party'], overridePermission: 'requests.admin'",
		"decrypted: ['terms']",
		"reveal: { terms: 'contracts.reveal' }",
	} {
		if !strings.Contains(router, want) {
			t.Errorf("router.ts lacks %s", want)
		}
	}
}

// accessCases are the cases access_runtime.test.ts runs, by name: D156's
// "done when" in the TypeScript server.
var accessCases = []string{
	"a calendar feed shows only the rows its token's principal reads, busy or in full, and a rotated or revoked one is refused",
	"a private row never appears to another user in a list, a read or a search",
	"a named row is visible to its owner and to the principals it names",
	"a document inherits its folder's audience, and follows it when the folder's changes",
	"an agent run sees what whom it acts for sees, and writes no read-only row",
	"a row rule follows a path through the table's relations",
	"a party with a grant reaches only the rows its role's paths allow",
	"a role whose path is * reaches every row of the table, the grant's target among them",
	"an expired or revoked grant is refused on the very next call",
	"a reveal opens the value for a holder of its permission and writes an access-log row",
}

// TestGeneratedAccessEndToEnd serves fixture-access-api's generated router
// under bun over fixture-access-db's generated repositories on Postgres,
// in a schema of its own at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL with
// the database's create.sql applied, and drives it over HTTP
// (testdata/access_runtime.test.ts): every case of accessCases must pass.
// It skips without the database.
func TestGeneratedAccessEndToEnd(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated TypeScript server's access against Postgres")
	}
	fixture := accessFixture(t)
	tree := materializeAPI(t, fixture)
	tree.typeCheck(t)

	ddl, err := sqlgen.Generate(fixture.deps[accessDB], sqlgen.Options{SchemaName: accessDB})
	if err != nil {
		t.Fatalf("generate %s's DDL: %v", accessDB, err)
	}
	ddlDir := filepath.Join(tree.root, "sql", accessDB)
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatalf("write %s's DDL: %v", accessDB, err)
	}
	createSQL := filepath.Join(ddlDir, "test-create.sql")
	pgtest.WriteCreateSQL(t, filepath.Join(ddlDir, "create.sql"), createSQL)

	testFile := tree.stage(t, "access_runtime.test.ts")
	report := filepath.Join(tree.root, "access.junit.xml")
	run := exec.Command(tree.bun, "test", testFile, "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = tree.apiDir
	run.Env = append(os.Environ(), "API_DIR="+tree.apiDir, "CREATE_SQL="+createSQL, "SUPERSCHEMATIC_RULES_TEST_DATABASE_URL="+dsn)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("access_runtime.test.ts failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range accessCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(accessCases) {
		t.Errorf("bun ran %d cases, and accessCases names %d:\n%s", len(cases), len(accessCases), junit)
	}
	if testing.Verbose() {
		t.Logf("access_runtime.test.ts:\n%s", out)
	}
}

// TestGeneratedAccessEndToEndOnSQLite serves fixture-access-api's
// generated router under bun over fixture-access-db's generated
// repositories on SQLite (D182): the ./orm/sqlite entry's createOrm on
// openSqlite's connection to a file its sqlite/create.sql made. It runs
// access_runtime.test.ts with ORM_DIALECT sqlite, every case of
// accessCases, which needs no database server.
func TestGeneratedAccessEndToEndOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated TypeScript server's access on SQLite in -short mode")
	}
	fixture := accessFixture(t)
	fixture.sqlite = true
	tree := materializeAPI(t, fixture)
	tree.typeCheck(t)

	model, err := sqlmigrate.BuildModel(fixture.deps[accessDB], sqlgen.Options{SchemaName: accessDB}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("build %s's SQLite model: %v", accessDB, err)
	}
	script, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatalf("write %s's SQLite create.sql: %v", accessDB, err)
	}
	createSQL := filepath.Join(tree.root, "sql", accessDB, "sqlite", "create.sql")
	if err := os.MkdirAll(filepath.Dir(createSQL), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(createSQL, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	testFile := tree.stage(t, "access_runtime.test.ts")
	report := filepath.Join(tree.root, "access-sqlite.junit.xml")
	run := exec.Command(tree.bun, "test", testFile, "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = tree.apiDir
	run.Env = append(os.Environ(), "API_DIR="+tree.apiDir, "CREATE_SQL="+createSQL, "ORM_DIALECT=sqlite", "SQLITE_FILE="+filepath.Join(tree.root, "access.sqlite"))
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("access_runtime.test.ts on SQLite failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range accessCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass on SQLite:\n%s", name, junit)
		}
	}
	if len(cases) != len(accessCases) {
		t.Errorf("bun ran %d cases, and accessCases names %d:\n%s", len(cases), len(accessCases), junit)
	}
	if testing.Verbose() {
		t.Logf("access_runtime.test.ts on SQLite:\n%s", out)
	}
}
