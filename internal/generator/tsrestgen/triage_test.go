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
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// triageAPI is the API whose @triageOperations set the loader fills with
// the inbound path's triage of fixture-parties-db (#746), and triageDB its
// database.
const (
	triageAPI = "fixture-triage-api"
	triageDB  = "fixture-parties-db"
)

// triageDeps is fixture-triage-api's Deps as the dispatch layer sets it: a
// pool of its database, the repositories its TypeScript types export, and
// the notifier and the inbound path the runtime builds over them.
var triageDeps = DepsInfo{
	Database:             triageDB,
	Dialect:              "postgres",
	ORM:                  "FixturePartiesDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-parties-db-types",
	Notifier:             &DepsNotifier{Dispatch: "NoticeWork"},
	Inbound:              &DepsInbound{Runs: "RunRequest"},
}

// triageFixture is fixture-triage-api with its database and apigen's
// endpoints for it.
func triageFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, triageDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", triageDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, triageAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", triageAPI, err)
	}
	deps := map[string]*ir.Schema{triageDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     triageAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: triageAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, depsInfo: triageDeps}
}

// generateTriage generates the package for fixture-triage-api, its Deps
// set.
func generateTriage(t *testing.T, fixture apiFixture) *APIOutput {
	t.Helper()
	output, err := Generate(fixture.schema, fixture.endpoints, Options{SchemaName: fixture.name, Dependencies: fixture.deps, Clock: fixedClock})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := output.SetDeps(triageDeps); err != nil {
		t.Fatalf("SetDeps: %v", err)
	}
	return output
}

// TestWriteAPIGoldenTriage pins the package for fixture-triage-api: its
// implementation interface has listContacts alone, and its router mounts
// each triage operation with the runtime's triageOperation over
// RouterOptions.inbound, the list with its page. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenTriage -update
func TestWriteAPIGoldenTriage(t *testing.T) {
	checkGolden(t, generateTriage(t, triageFixture(t)), triageAPI)
}

// TestGenerateTriage: a triage operation is the router's, not the
// implementation's; its endpoint carries its marker; and Deps without the
// inbound path is refused.
func TestGenerateTriage(t *testing.T) {
	fixture := triageFixture(t)
	output := generateTriage(t, fixture)
	if !output.HasTriage() || !output.HasPages {
		t.Errorf("HasTriage %t, HasPages %t", output.HasTriage(), output.HasPages)
	}
	var routes []string
	for _, route := range output.TriageRoutes() {
		routes = append(routes, route.Messages+"/"+route.Party+"."+route.Operation)
	}
	if !slices.Equal(routes, []string{"Message/Contact.triage", "Message/Contact.triageMessage", "Message/Contact.dismiss", "Message/Contact.settleOnto"}) {
		t.Errorf("TriageRoutes = %v", routes)
	}
	for _, ns := range output.Namespaces {
		for _, ep := range ns.Endpoints {
			if ep.Triage != nil {
				t.Errorf("the implementation interface %s has %s, a triage operation", ns.InterfaceName, ep.Name)
			}
		}
	}
	if !strings.Contains(output.RuntimeImport(), "triageOperation") || !strings.Contains(output.RuntimeImport(), "type TriageInbound") {
		t.Errorf("RuntimeImport = %s", output.RuntimeImport())
	}
	without := triageDeps
	without.Inbound = nil
	fresh, err := Generate(fixture.schema, fixture.endpoints, Options{SchemaName: fixture.name, Dependencies: fixture.deps, Clock: fixedClock})
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.SetDeps(without); err == nil || !strings.Contains(err.Error(), "its Deps holds no inbound path") {
		t.Errorf("SetDeps without the inbound path = %v", err)
	}
}

// triageCheck is the file tsc checks the package for fixture-triage-api
// against beside it: buildRouter takes the inbound path the runtime
// builds, and refuses options without it; and the implementation has no
// method for a triage operation.
const triageCheck = `import type { Inbound } from '@superschematic/http-runtime/postgres';
import { buildRouter, triageOperations, type Implementations } from './index';

declare const implementations: Implementations;
declare const inbound: Inbound;

export const router = buildRouter(implementations, { inbound });
// @ts-expect-error RouterOptions.inbound runs the triage.
export const without = buildRouter(implementations, {});
// @ts-expect-error The triage operations have no implementation: Implementations holds none of them.
export const settle: Implementations['triage'] = undefined;
export const operation: 'settleOnto' = triageOperations.settleOnto.operation;
`

// TestGeneratedTriageAPICompiles type-checks the package for
// fixture-triage-api against its type packages, the real runtime and Hono,
// with triageCheck.
func TestGeneratedTriageAPICompiles(t *testing.T) {
	tree := materializeAPI(t, triageFixture(t))
	if err := os.WriteFile(filepath.Join(tree.apiDir, "triage.check.ts"), []byte(triageCheck), 0o644); err != nil {
		t.Fatal(err)
	}
	tree.typeCheck(t)
}

// triageCases are the cases triage_runtime.test.ts runs, by name: #606's
// triage through the generated routes.
var triageCases = []string{
	"a held message is listed only to a caller who holds the triage permission, a page at a time and by channel",
	"a held message shows its sender, its channel, its body and its files",
	"settling a held message onto a party files it as the party's and takes it out of triage",
	"settling onto a party the caller cannot read, or onto an address that is a user's, is refused",
	"dismissing a held message takes it out of triage, and a second dismiss or a settle of it is a conflict",
}

// TestGeneratedTriageEndToEnd serves fixture-triage-api's generated
// router under bun over fixture-parties-db's generated repositories on
// Postgres, in a schema of its own at
// SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL with the database's create.sql
// applied, with the inbound path the runtime builds over them and a bucket
// on an R2 binding over a map, and drives the triage over HTTP
// (testdata/triage_runtime.test.ts): every case of triageCases must pass.
// It skips without the database, and without bun unless
// SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must run.
func TestGeneratedTriageEndToEnd(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL to run the generated TypeScript server's triage against Postgres")
	}
	fixture := triageFixture(t)
	tree := materializeAPI(t, fixture)
	tree.typeCheck(t)

	ddl, err := sqlgen.Generate(fixture.deps[triageDB], sqlgen.Options{SchemaName: triageDB})
	if err != nil {
		t.Fatalf("generate %s's DDL: %v", triageDB, err)
	}
	ddlDir := filepath.Join(tree.root, "sql", triageDB)
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatalf("write %s's DDL: %v", triageDB, err)
	}
	createSQL := filepath.Join(ddlDir, "test-create.sql")
	pgtest.WriteCreateSQL(t, filepath.Join(ddlDir, "create.sql"), createSQL)

	testFile := tree.stage(t, "triage_runtime.test.ts")
	report := filepath.Join(tree.root, "triage.junit.xml")
	run := exec.Command(tree.bun, "test", testFile, "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = tree.apiDir
	run.Env = append(os.Environ(), "API_DIR="+tree.apiDir, "CREATE_SQL="+createSQL, "SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL="+dsn)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("triage_runtime.test.ts failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range triageCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(triageCases) {
		t.Errorf("bun ran %d cases, and triageCases names %d:\n%s", len(cases), len(triageCases), junit)
	}
	if testing.Verbose() {
		t.Logf("triage_runtime.test.ts:\n%s", out)
	}
}
