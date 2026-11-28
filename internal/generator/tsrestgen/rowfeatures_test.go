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

// rowFeaturesAPI is the API whose @rowFeatureOperations sets the loader
// fills with the references, backlinks and files of
// fixture-references-db's Ticket and Note (#723), and rowFeaturesDB its
// database.
const (
	rowFeaturesAPI = "fixture-references-api"
	rowFeaturesDB  = "fixture-references-db"
)

// rowFeaturesFixture is fixture-references-api with its database and
// apigen's endpoints for it; the router serves its MCP tools.
func rowFeaturesFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, rowFeaturesDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", rowFeaturesDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, rowFeaturesAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", rowFeaturesAPI, err)
	}
	deps := map[string]*ir.Schema{rowFeaturesDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     rowFeaturesAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: rowFeaturesAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, mcp: true, depsInfo: rowFeaturesDeps}
}

// rowFeaturesDeps is fixture-references-api's Deps as the dispatch layer
// sets it: a pool of its database, which lists postgres first, the
// repositories its TypeScript types export, and its one bucket.
var rowFeaturesDeps = DepsInfo{
	Database:             rowFeaturesDB,
	Dialect:              "postgres",
	ORM:                  "FixtureReferencesDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-references-db-types",
	Buckets:              []DepsBucket{{Service: "fixture-references-files", Field: "fixtureReferencesFiles"}},
}

// generateRowFeatures generates the package for fixture-references-api,
// its Deps set.
func generateRowFeatures(t *testing.T, fixture apiFixture) *APIOutput {
	t.Helper()
	output, err := Generate(fixture.schema, fixture.endpoints, Options{SchemaName: fixture.name, Dependencies: fixture.deps, Clock: fixedClock, MCP: fixture.mcp})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := output.SetDeps(rowFeaturesDeps); err != nil {
		t.Fatalf("SetDeps: %v", err)
	}
	return output
}

// TestWriteAPIGoldenRowFeatures pins the package for
// fixture-references-api: its implementation interface has getTicket
// alone, and its router mounts each row feature operation with the
// runtime's rowFeatureOperation over RouterOptions.orm, the file
// operations that sign a URL over RouterOptions.attachmentsBucket.
// Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenRowFeatures -update
func TestWriteAPIGoldenRowFeatures(t *testing.T) {
	checkGolden(t, generateRowFeatures(t, rowFeaturesFixture(t)), rowFeaturesAPI)
}

// TestGenerateRowFeatures: a row feature operation is the router's, not
// the implementation's; its endpoint carries its marker; the bucket is
// the API's one; and Deps without a member for it is refused.
func TestGenerateRowFeatures(t *testing.T) {
	fixture := rowFeaturesFixture(t)
	output := generateRowFeatures(t, fixture)
	if !output.HasRowFeatures() || !output.RouterNeedsORM() || output.HasBehaviors() {
		t.Errorf("HasRowFeatures %t, RouterNeedsORM %t", output.HasRowFeatures(), output.RouterNeedsORM())
	}
	if what, decisions := output.ORMOperations(); what != "the references and files" || decisions != "#723" {
		t.Errorf("ORMOperations = %q, %q", what, decisions)
	}
	if got := output.RowFeatureBucket(); got != "fixture-references-files" {
		t.Errorf("RowFeatureBucket = %q", got)
	}
	if got := output.RowFeatureBucketField(); got != "fixtureReferencesFiles" {
		t.Errorf("RowFeatureBucketField = %q", got)
	}
	var routes []string
	for _, route := range output.RowFeatureRoutes() {
		routes = append(routes, route.Type+"."+route.Operation)
	}
	if len(routes) != 19 || !slices.Contains(routes, "Ticket.removeAttachment") || slices.Contains(routes, "Note.removeAttachment") {
		t.Errorf("RowFeatureRoutes = %v", routes)
	}
	for _, ns := range output.Namespaces {
		for _, ep := range ns.Endpoints {
			if ep.RowFeature != nil {
				t.Errorf("the implementation interface %s has %s, a row feature operation", ns.InterfaceName, ep.Name)
			}
		}
	}
	without := rowFeaturesDeps
	without.Buckets = nil
	fresh, err := Generate(fixture.schema, fixture.endpoints, Options{SchemaName: fixture.name, Dependencies: fixture.deps, Clock: fixedClock})
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.SetDeps(without); err == nil || !strings.Contains(err.Error(), "Deps holds no member for it") {
		t.Errorf("SetDeps without the bucket = %v", err)
	}
}

// rowFeaturesCheck is the file tsc checks the package for
// fixture-references-api against beside it: buildRouter takes the
// repositories and the bucket, and refuses options without either; and the
// implementation has no method for a row feature operation.
const rowFeaturesCheck = `import type { Bucket } from '@superschematic/http-runtime';
import type { FixtureReferencesDbOrm } from '@schemas/fixture-references-db-types/orm';
import { buildRouter, rowFeatureOperations, type Implementations } from './index';

declare const implementations: Implementations;
declare const orm: FixtureReferencesDbOrm;
declare const attachmentsBucket: Bucket;

export const router = buildRouter(implementations, { orm, attachmentsBucket });
// @ts-expect-error RouterOptions.attachmentsBucket holds the bucket the uploads sign a URL of.
export const without = buildRouter(implementations, { orm });
// @ts-expect-error RouterOptions.orm runs the references and files.
export const withoutOrm = buildRouter(implementations, { attachmentsBucket });
// @ts-expect-error The row feature operations have no implementation method.
export const backlinks: Implementations['ticket']['backlinks'] = undefined;
export const operation: 'backlinks' = rowFeatureOperations.ticketBacklinks.operation;
`

// TestGeneratedRowFeaturesAPICompiles type-checks the package for
// fixture-references-api against its type packages, the real runtime and
// Hono, with rowFeaturesCheck.
func TestGeneratedRowFeaturesAPICompiles(t *testing.T) {
	tree := materializeAPI(t, rowFeaturesFixture(t))
	if err := os.WriteFile(filepath.Join(tree.apiDir, "rowfeatures.check.ts"), []byte(rowFeaturesCheck), 0o644); err != nil {
		t.Fatal(err)
	}
	tree.typeCheck(t)
}

// rowFeaturesCases are the cases rowfeatures_runtime.test.ts runs, by
// name: D154's done-when through the generated routes.
var rowFeaturesCases = []string{
	"a file uploaded straight to the bucket through the signed URL and posted on one row is filed onto a second row, which lists the first in its backlinks",
	"a caller who cannot read a referenced row sees neither the reference nor a count, and its owner sees a count",
	"a reference is added, listed and removed through the routes",
	"an upload whose bytes differ is refused, and a file of another row is not found",
	"a permission the set names for an operation replaces the class's, and a call with no caller is refused",
	"the MCP tools are the reads and the add of a reference",
}

// TestGeneratedRowFeaturesEndToEnd serves fixture-references-api's
// generated router under bun over fixture-references-db's generated
// repositories on Postgres, in a schema of its own at
// SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL with the database's create.sql
// applied, and a bucket on an R2 binding whose S3 API a stand-in serves,
// checking each PUT against the URL's V4 signature; and drives it through
// the API's generated TypeScript SDK and its MCP endpoint over HTTP
// (testdata/rowfeatures_runtime.test.ts): every case of rowFeaturesCases
// must pass. It skips without the database, and without bun unless
// SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must run.
func TestGeneratedRowFeaturesEndToEnd(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL to run the generated TypeScript server's references and files against Postgres")
	}
	fixture := rowFeaturesFixture(t)
	tree := materializeAPI(t, fixture)
	tree.typeCheck(t)

	names := naming.Default()
	tsOutput, err := tsgen.Generate(fixture.schema, tsgen.Options{
		SchemaName:         rowFeaturesAPI,
		Dependencies:       fixture.deps,
		DependencyPackages: map[string]string{rowFeaturesDB: names.NpmTypesPackage(rowFeaturesDB)},
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
	sdkDir := filepath.Join(tree.root, "sdk", "typescript", rowFeaturesAPI)
	if err := sdkgen.WriteSDKWithTools(sdkOutput, fixture.endpoints, sdkDir, fixedClock); err != nil {
		t.Fatalf("write the SDK: %v", err)
	}
	typesPackage := names.NpmTypesPackage(rowFeaturesAPI)
	link(t, tree.typesDirs[typesPackage], filepath.Join(sdkDir, "node_modules", filepath.FromSlash(typesPackage)))

	ddl, err := sqlgen.Generate(fixture.deps[rowFeaturesDB], sqlgen.Options{SchemaName: rowFeaturesDB})
	if err != nil {
		t.Fatalf("generate %s's DDL: %v", rowFeaturesDB, err)
	}
	ddlDir := filepath.Join(tree.root, "sql", rowFeaturesDB)
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatalf("write %s's DDL: %v", rowFeaturesDB, err)
	}
	createSQL := filepath.Join(ddlDir, "test-create.sql")
	pgtest.WriteCreateSQL(t, filepath.Join(ddlDir, "create.sql"), createSQL)

	testFile := tree.stage(t, "rowfeatures_runtime.test.ts")
	report := filepath.Join(tree.root, "rowfeatures.junit.xml")
	run := exec.Command(tree.bun, "test", testFile, "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = tree.apiDir
	run.Env = append(os.Environ(), "API_DIR="+tree.apiDir, "SDK_DIR="+sdkDir, "CREATE_SQL="+createSQL, "SUPERSCHEMATIC_POSTGRES_TEST_DATABASE_URL="+dsn)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("rowfeatures_runtime.test.ts failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range rowFeaturesCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(rowFeaturesCases) {
		t.Errorf("bun ran %d cases, and rowFeaturesCases names %d:\n%s", len(cases), len(rowFeaturesCases), junit)
	}
	if testing.Verbose() {
		t.Logf("rowfeatures_runtime.test.ts:\n%s", out)
	}
}
