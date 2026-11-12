package tsrestgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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

// trackerAPI is the API whose operations the build serves from the tables
// of fixture-tracker-db (D106): teams, members, a team's issues, which
// are audited, soft-deleted, searched, sorted and filtered, and an issue's
// comments, which are hard-deleted. Teams.summarizeTeam alone is written
// by hand, in a set with table operations.
const trackerAPI = "fixture-tracker-api"

// trackerDB is fixture-tracker-api's database, its one DB dependency.
const trackerDB = "fixture-tracker-db"

// trackerFixture is fixture-tracker-api with its database and apigen's
// endpoints for it; rewrite edits the loaded API first when set, as IR
// that did not go through the loader's checks would stand.
func trackerFixture(t *testing.T, rewrite func(*ir.Schema)) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, trackerDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", trackerDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, trackerAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", trackerAPI, err)
	}
	if rewrite != nil {
		rewrite(schema)
	}
	deps := map[string]*ir.Schema{trackerDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     trackerAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: trackerAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, depsInfo: trackerDeps}
}

// trackerDeps is fixture-tracker-api's Deps as the dispatch layer sets it:
// a pool of its database and the repositories its TypeScript types export.
var trackerDeps = DepsInfo{
	Database:             trackerDB,
	Dialect:              "postgres",
	ORM:                  "FixtureTrackerDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-tracker-db-types",
}

// generateTracker generates the package for fixture, without setting its
// Deps.
func generateTracker(t *testing.T, fixture apiFixture) *APIOutput {
	t.Helper()
	output, err := Generate(fixture.schema, fixture.endpoints, Options{SchemaName: fixture.name, Dependencies: fixture.deps, Clock: fixedClock})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return output
}

// generateTrackerFixture generates the package for fixture-tracker-api,
// its Deps set.
func generateTrackerFixture(t *testing.T) *APIOutput {
	t.Helper()
	output := generateTracker(t, trackerFixture(t, nil))
	if err := output.SetDeps(trackerDeps); err != nil {
		t.Fatalf("SetDeps: %v", err)
	}
	return output
}

// TestWriteAPIGoldenTableOperations pins the package for
// fixture-tracker-api (D106): its implementation interface and its
// scaffold have summarizeTeam alone, and its router mounts each table
// operation with the runtime's handler of its kind over
// RouterOptions.orm. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenTableOperations -update
func TestWriteAPIGoldenTableOperations(t *testing.T) {
	checkGolden(t, generateTrackerFixture(t), trackerAPI)
}

// TestGenerateTableOperations: a table operation (D106) is the router's,
// not the implementation's. Its endpoint carries what the runtime's
// handler reads: the kind, the table and its repository, the key and the
// parents its path names, the members of its result's view and of its
// body, and a list's search argument. The implementation, its scaffold
// and interfaces.ts's imports hold summarizeTeam alone, which shares a
// set with table operations; and the router imports the handlers it
// mounts, the page's and the filter's readers for the lists, and no type
// of a table operation's.
func TestGenerateTableOperations(t *testing.T) {
	output := generateTrackerFixture(t)
	if !output.HasTableOperations() || !output.RouterNeedsORM() || output.HasBehaviors() {
		t.Errorf("HasTableOperations %t, RouterNeedsORM %t, HasBehaviors %t", output.HasTableOperations(), output.RouterNeedsORM(), output.HasBehaviors())
	}
	issueView := []string{"id", "teamId", "title", "body", "status", "priority", "estimate", "dueOn", "assigneeId", "createdAt", "createdBy", "updatedAt"}
	commentView := []string{"id", "issueId", "body", "createdAt"}
	teamView := []string{"id", "key", "name"}
	noteView := []string{"id", "issueId", "owner", "sharedWith", "text", "createdAt", "createdBy"}
	want := map[string]*TableRoute{
		"listTeams":        {Operation: "Teams.listTeams", Kind: "list", Type: "Team", Repository: "team", View: teamView},
		"getTeam":          {Operation: "Teams.getTeam", Kind: "read", Type: "Team", Repository: "team", Key: "id", View: teamView},
		"createTeam":       {Operation: "Teams.createTeam", Kind: "create", Type: "Team", Repository: "team", View: teamView, Body: []string{"key", "name"}},
		"summarizeTeam":    nil,
		"listMembers":      {Operation: "Members.listMembers", Kind: "list", Type: "Member", Repository: "member", View: []string{"id", "name"}},
		"createMember":     {Operation: "Members.createMember", Kind: "create", Type: "Member", Repository: "member", View: []string{"id", "name"}, Body: []string{"name"}},
		"listIssues":       {Operation: "Issues.listIssues", Kind: "list", Type: "Issue", Repository: "issue", Parents: []string{"teamId"}, View: issueView, Search: "q"},
		"getIssue":         {Operation: "Issues.getIssue", Kind: "read", Type: "Issue", Repository: "issue", Key: "id", View: issueView},
		"getIssueForGuest": {Operation: "Issues.getIssueForGuest", Kind: "read", Type: "Issue", Repository: "issue", Key: "id", View: []string{"id", "title", "status"}},
		"createIssue":      {Operation: "Issues.createIssue", Kind: "create", Type: "Issue", Repository: "issue", Parents: []string{"teamId"}, View: issueView, Body: []string{"title", "body", "priority", "estimate", "dueOn", "assigneeId"}},
		"updateIssue":      {Operation: "Issues.updateIssue", Kind: "update", Type: "Issue", Repository: "issue", Key: "id", View: issueView, Body: []string{"title", "body", "status", "priority", "estimate", "dueOn", "assigneeId"}},
		"deleteIssue":      {Operation: "Issues.deleteIssue", Kind: "delete", Type: "Issue", Repository: "issue", Key: "id", View: issueView},
		// A row rule is the router's to hold (D108): its members, and the
		// permission that lifts it.
		"listNotes":     {Operation: "Notes.listNotes", Kind: "list", Type: "Note", Repository: "note", View: noteView, By: []string{"owner", "sharedWith"}, Override: "notes.admin"},
		"getNote":       {Operation: "Notes.getNote", Kind: "read", Type: "Note", Repository: "note", Key: "id", View: noteView, By: []string{"owner", "sharedWith"}, Override: "notes.admin"},
		"createNote":    {Operation: "Notes.createNote", Kind: "create", Type: "Note", Repository: "note", Parents: []string{"issueId"}, View: noteView, Body: []string{"text", "sharedWith"}, By: []string{"owner"}},
		"updateNote":    {Operation: "Notes.updateNote", Kind: "update", Type: "Note", Repository: "note", Key: "id", View: noteView, Body: []string{"text", "sharedWith"}, By: []string{"owner"}},
		"deleteNote":    {Operation: "Notes.deleteNote", Kind: "delete", Type: "Note", Repository: "note", Key: "id", View: noteView, By: []string{"createdBy"}, Override: "notes.admin"},
		"listComments":  {Operation: "Comments.listComments", Kind: "list", Type: "Comment", Repository: "comment", Parents: []string{"issueId"}, View: commentView},
		"createComment": {Operation: "Comments.createComment", Kind: "create", Type: "Comment", Repository: "comment", Parents: []string{"issueId"}, View: commentView, Body: []string{"body"}},
		"deleteComment": {Operation: "Comments.deleteComment", Kind: "delete", Type: "Comment", Repository: "comment", Key: "id", Parents: []string{"issueId"}, View: commentView},
	}
	byName := endpointsByName(output)
	if len(byName) != len(want) {
		t.Errorf("the router has %d operations, want %d", len(byName), len(want))
	}
	for name, route := range want {
		ep, ok := byName[name]
		if !ok {
			t.Errorf("no endpoint %s", name)
			continue
		}
		if route != nil {
			route.Database = trackerDB
		}
		if !reflect.DeepEqual(ep.Table, route) {
			t.Errorf("%s's table operation = %+v, want %+v", name, ep.Table, route)
		}
		if ep.Key != name || ep.Manual() {
			t.Errorf("%s is keyed %s, manual %t", name, ep.Key, ep.Manual())
		}
	}
	for name, literal := range map[string]string{
		"getTeam":       "{ kind: 'read', type: 'Team', repository: 'team', key: 'id', view: ['id', 'key', 'name'] }",
		"createTeam":    "{ kind: 'create', type: 'Team', repository: 'team', view: ['id', 'key', 'name'], body: ['key', 'name'] }",
		"listIssues":    "{ kind: 'list', type: 'Issue', repository: 'issue', parents: ['teamId'], view: ['id', 'teamId', 'title', 'body', 'status', 'priority', 'estimate', 'dueOn', 'assigneeId', 'createdAt', 'createdBy', 'updatedAt'], search: 'q' }",
		"deleteComment": "{ kind: 'delete', type: 'Comment', repository: 'comment', key: 'id', parents: ['issueId'], view: ['id', 'issueId', 'body', 'createdAt'] }",
	} {
		if got := byName[name].Table.Literal(); got != literal {
			t.Errorf("%s's literal = %s, want %s", name, got, literal)
		}
	}
	if got, want := byName["updateIssue"].Table.Handler()+" "+byName["updateIssue"].Table.Article(), "tableUpdate An update"; got != want {
		t.Errorf("updateIssue is %q, want %q", got, want)
	}

	// The routes are decoded as an implemented route's: the list keeps
	// its page, its sort, its filter and its search argument.
	list := byName["listIssues"]
	if list.Page == nil || list.Page.Sort == nil || list.Filter == nil || len(list.QueryParams) != 1 || list.QueryParams[0].Name != "q" {
		t.Errorf("listIssues: page %+v, filter %+v, query %+v", list.Page, list.Filter, list.QueryParams)
	}
	if update := byName["updateIssue"]; !update.HasInput || update.InputValidator != "parseUpdateIssueInputJson" || update.Method != "PATCH" {
		t.Errorf("updateIssue = %+v", update)
	}

	// The implementation has summarizeTeam alone: the sets whose
	// operations are all the build's have none.
	if len(output.Namespaces) != 1 || output.Namespaces[0].Name != "teams" || len(output.Namespaces[0].Endpoints) != 1 || output.Namespaces[0].Endpoints[0].Name != "summarizeTeam" {
		t.Errorf("namespaces = %+v", output.Namespaces)
	}
	imported := func(imports []PackageImport) []string {
		var out []string
		for _, imp := range imports {
			out = append(out, imp.Symbols...)
		}
		return out
	}
	if got := imported(output.TypeImports); !slices.Equal(got, []string{"TeamSummary"}) {
		t.Errorf("interfaces.ts imports %v, want TeamSummary alone", got)
	}
	if got := imported(output.RouterTypeImports); len(got) != 0 {
		t.Errorf("router.ts imports the types %v, and no cast of a table operation's names one", got)
	}
	for _, validator := range []string{"parseCreateIssueInputJson", "parseUpdateIssueInputFromJSON", "parseCreateTeamInputJson"} {
		if !slices.Contains(imported(output.ValidatorImports), validator) {
			t.Errorf("router.ts does not import %s: %+v", validator, output.ValidatorImports)
		}
	}
	if got, want := output.InterfaceRuntimeTypes(), []string{"OperationResult", "RequestContext"}; !slices.Equal(got, want) {
		t.Errorf("interfaces.ts imports %v from the runtime, want %v: no implemented operation pages or filters", got, want)
	}
	if output.ImplementsPages() || len(output.FilteredEndpoints()) != 0 || !output.ReadsFilters() || !output.HasPages || !output.HasSortedPages || !output.HasFilters {
		t.Errorf("ImplementsPages %t, FilteredEndpoints %d, ReadsFilters %t, HasPages %t, HasSortedPages %t, HasFilters %t",
			output.ImplementsPages(), len(output.FilteredEndpoints()), output.ReadsFilters(), output.HasPages, output.HasSortedPages, output.HasFilters)
	}
	wantImport := "import { bindPage, readFilter, readPage, tableCreate, tableDelete, tableList, tableRead, tableUpdate, type FilterSpec, type OperationSpec, type PageSpec, type TableOperation } from '@superschematic/http-runtime';"
	if got := output.RuntimeImport(); got != wantImport {
		t.Errorf("router.ts's runtime import =\n%s\nwant\n%s", got, wantImport)
	}
	if what, decisions := output.ORMOperations(); what != "the table operations" || decisions != "D106" {
		t.Errorf("the ORM runs %q (%s)", what, decisions)
	}

	// An API without one is as it was.
	plain := generateFixtureAPI(t)
	if plain.HasTableOperations() || plain.RouterNeedsORM() || len(plain.TableHandlers()) != 0 || strings.Contains(plain.RuntimeImport(), "able") {
		t.Errorf("fixture-api, which marks no @tableOperation: %s", plain.RuntimeImport())
	}
}

// TestTableOperationsNeedTheRepositories: the table operations run
// through Deps.orm, so Deps without the repositories is refused, saying
// why there are none, and so is a package written without Deps.
func TestTableOperationsNeedTheRepositories(t *testing.T) {
	const prefix = "tsrestgen: fixture-tracker-api serves Issues.deleteIssue, a @tableOperation (a delete of Issue), which runs through Deps.orm, the repositories fixture-tracker-db's TypeScript types export for Postgres; "
	for name, tc := range map[string]struct {
		deps DepsInfo
		why  string
	}{
		"no TypeScript types": {DepsInfo{Database: trackerDB, Dialect: "postgres"}, "enable outputs.types.typescript in fixture-tracker-db's config"},
		"sqlite first":        {DepsInfo{Database: trackerDB, Dialect: SQLiteDialect, ORM: "FixtureTrackerDbOrm", DatabaseTypesPackage: "@schemas/fixture-tracker-db-types"}, "fixture-tracker-db lists sqlite first, and the repositories run on Postgres alone (D106)"},
	} {
		t.Run(name, func(t *testing.T) {
			err := generateTracker(t, trackerFixture(t, nil)).SetDeps(tc.deps)
			if err == nil || err.Error() != prefix+tc.why {
				t.Errorf("SetDeps = %v,\nwant %s", err, prefix+tc.why)
			}
		})
	}
	err := WriteAPI(generateTracker(t, trackerFixture(t, nil)), t.TempDir())
	if err == nil || !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("WriteAPI without Deps = %v", err)
	}
}

// TestTableOperationsTheLoaderDidNotFill: the loader fills an operation's
// tableOperation and refuses what no handler serves, so IR that reaches
// the generator without it is refused, naming the operation, and never
// served by something else.
func TestTableOperationsTheLoaderDidNotFill(t *testing.T) {
	operation := func(schema *ir.Schema, name string) *ir.FieldDef {
		for _, set := range schema.OperationSets {
			for _, op := range set.Operations {
				if op.Name == name {
					return op
				}
			}
		}
		t.Fatalf("no operation %s", name)
		return nil
	}
	const suffix = "; the loader fills an operation's tableOperation and checks it, so load the schema before generating"
	for name, tc := range map[string]struct {
		rewrite func(*ir.Schema)
		want    string
	}{
		"an empty record": {
			func(schema *ir.Schema) { operation(schema, "getIssue").TableOperation = &ir.TableOperation{} },
			`tsrestgen: Issues.getIssue is a @tableOperation (D106) whose kind, table and database are not all set (kind "", type "", database "")`,
		},
		"a kind no handler has": {
			func(schema *ir.Schema) { operation(schema, "getIssue").TableOperation.Kind = "upsert" },
			`tsrestgen: Issues.getIssue is a @tableOperation (D106) whose kind, table and database are not all set (kind "upsert", type "Issue", database "fixture-tracker-db")`,
		},
		"a read without its key": {
			func(schema *ir.Schema) { operation(schema, "getIssue").TableOperation.Key = "" },
			`tsrestgen: Issues.getIssue is a @tableOperation (D106) whose route, GET /api/issues/{id}, has no path parameter for the key of Issue ("")`,
		},
		"a parent the path lacks": {
			func(schema *ir.Schema) {
				operation(schema, "getIssue").TableOperation.Parents = []ir.TableOperationParent{{Param: "teamId", Relation: "team"}}
			},
			`tsrestgen: Issues.getIssue is a @tableOperation (D106) whose route, GET /api/issues/{id}, has no path parameter "teamId", a parent's`,
		},
		"a path parameter that is neither": {
			func(schema *ir.Schema) { operation(schema, "createIssue").TableOperation.Parents = nil },
			`tsrestgen: Issues.createIssue is a @tableOperation (D106) whose route, POST /api/teams/{teamId}/issues, has a path parameter that is neither its key nor a parent`,
		},
		"a create without its body": {
			func(schema *ir.Schema) { operation(schema, "createIssue").TableOperation.Input = "" },
			`tsrestgen: Issues.createIssue is a @tableOperation (D106) whose create takes no body the router decodes as an input type (input "")`,
		},
		"a list that does not page": {
			func(schema *ir.Schema) { operation(schema, "listTeams").Page = nil },
			`tsrestgen: Teams.listTeams is a @tableOperation (D106) that lists Team and is not @paginated`,
		},
		"a result that is no view": {
			func(schema *ir.Schema) {
				operation(schema, "summarizeTeam").TableOperation = &ir.TableOperation{Kind: "read", Database: trackerDB, Type: "Team", Key: "id"}
			},
			`tsrestgen: Teams.summarizeTeam is a @tableOperation (D106) whose result, TeamSummary, is no @source view of fixture-tracker-api's with fields`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := trackerFixture(t, tc.rewrite)
			_, err := Generate(fixture.schema, fixture.endpoints, Options{SchemaName: fixture.name, Dependencies: fixture.deps, Clock: fixedClock})
			if err == nil || err.Error() != tc.want+suffix {
				t.Errorf("Generate = %v,\nwant %s", err, tc.want+suffix)
			}
		})
	}
}

// tableCheck is the file tsc checks the package for fixture-tracker-api
// against beside it: buildRouter takes the repositories, and refuses
// options without them; the implementation has summarizeTeam alone, and
// no member for a set whose operations are all the build's; and the
// operation table and the table of table operations hold every operation
// and each one the build serves.
const tableCheck = `import type { FixtureTrackerDbOrm } from '@schemas/fixture-tracker-db-types/orm';
import type { TableOperation } from '@superschematic/http-runtime';
import { buildRouter, operationSpecs, tableOperations, type Implementations, type TeamsImplementation } from './index';

declare const implementations: Implementations;
declare const orm: FixtureTrackerDbOrm;

export const router = buildRouter(implementations, { orm, bodyLimits: { updateIssue: 1024 } });
// @ts-expect-error RouterOptions.orm runs the table operations.
export const without = buildRouter(implementations, {});
// @ts-expect-error A set whose operations are all table operations has no implementation.
export const issues: Implementations['issues'] = {};
export const methods: keyof TeamsImplementation = 'summarizeTeam';
// @ts-expect-error A table operation is no method of the implementation.
export const generated: keyof TeamsImplementation = 'getTeam';
export const implemented: keyof Implementations = 'teams';
export const spec: 'updateIssue' = operationSpecs.updateIssue.name;
export const update: TableOperation = tableOperations.updateIssue;
export const kind: 'list' = tableOperations.listIssues.kind;
// @ts-expect-error An operation the implementation writes is no table operation.
export const handWritten = tableOperations.summarizeTeam;
`

// TestGeneratedTableOperationsAPICompiles type-checks the package for
// fixture-tracker-api against its type packages, fixture-tracker-db's
// repositories among them, the real runtime and Hono, with tableCheck,
// and holds its implementation interface and its scaffold to
// summarizeTeam alone.
func TestGeneratedTableOperationsAPICompiles(t *testing.T) {
	tree := materializeAPI(t, trackerFixture(t, nil))
	if err := os.WriteFile(filepath.Join(tree.apiDir, "table.check.ts"), []byte(tableCheck), 0o644); err != nil {
		t.Fatal(err)
	}
	tree.typeCheck(t)

	interfaces, err := os.ReadFile(filepath.Join(tree.apiDir, "interfaces.ts"))
	if err != nil {
		t.Fatal(err)
	}
	output := generateTrackerFixture(t)
	implDir := filepath.Join(t.TempDir(), "implementation")
	if written, err := WriteImplementationScaffold(output, implDir); err != nil || !written {
		t.Fatalf("write implementation scaffold: %v, %v", written, err)
	}
	scaffold, err := os.ReadFile(filepath.Join(implDir, "index.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for file, source := range map[string]string{"interfaces.ts": string(interfaces), "the scaffold": string(scaffold)} {
		if !strings.Contains(source, "summarizeTeam") {
			t.Errorf("%s has no summarizeTeam:\n%s", file, source)
		}
		for _, ep := range output.Endpoints {
			if ep.Table != nil && strings.Contains(source, ep.Name+"(") {
				t.Errorf("%s has %s, a table operation:\n%s", file, ep.Name, source)
			}
		}
	}
}

// tableCases are the cases table_operations_runtime.test.ts runs, by
// name: D106's parity cases, which the Go server runs too.
var tableCases = []string{
	"a create answers the row's view, with the table's defaults and its caller stamped",
	"a create under a parent sets the relation from the path, and a parent that does not exist is not found",
	"a read answers the view of the row its key and its parents name",
	"a second view of the table shows only its own members, behind its own guard",
	"an update sets the members the body sets: absent leaves, null clears, and null on a column that takes none is a 400 at the member",
	"an update of no member writes nothing",
	"a unique key another row holds is a conflict",
	"a relation's key that names no row is a 400 at the member",
	"a soft delete keeps the row, stamped, and every operation then finds none",
	"a hard delete removes the row, and one under another parent is not found",
	"a list pages by cursor in the repository's order, under its parents",
	"a list sorts by each field either way, each next bound to its sort",
	"a list searches by its text",
	"a list filters by each kind of field",
	"a list's contains matches % and _ as themselves",
	"a list's filters, search, sort and cursor hold together",
	"the routes' guards hold: no caller, no permission, a public route",
	"a row rule keeps each caller to the rows that name them, and its override permission lifts it",
	"an operation written by hand reaches the implementation",
}

// TestGeneratedTableOperationsEndToEnd serves fixture-tracker-api's
// generated router under bun over fixture-tracker-db's generated
// repositories on Postgres, in a schema of its own at
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL with the database's create.sql
// applied, and drives it over HTTP
// (testdata/table_operations_runtime.test.ts): every case of tableCases
// must pass. It skips without the database, and without bun unless
// SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must run.
func TestGeneratedTableOperationsEndToEnd(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated TypeScript server's table operations against Postgres")
	}
	fixture := trackerFixture(t, nil)
	tree := materializeAPI(t, fixture)
	tree.typeCheck(t)

	ddl, err := sqlgen.Generate(fixture.deps[trackerDB], sqlgen.Options{SchemaName: trackerDB})
	if err != nil {
		t.Fatalf("generate %s's DDL: %v", trackerDB, err)
	}
	ddlDir := filepath.Join(tree.root, "sql", trackerDB)
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatalf("write %s's DDL: %v", trackerDB, err)
	}
	createSQL := filepath.Join(ddlDir, "test-create.sql")
	pgtest.WriteCreateSQL(t, filepath.Join(ddlDir, "create.sql"), createSQL)

	testFile := tree.stage(t, "table_operations_runtime.test.ts")
	report := filepath.Join(tree.root, "table_operations.junit.xml")
	run := exec.Command(tree.bun, "test", testFile, "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = tree.apiDir
	run.Env = append(os.Environ(), "API_DIR="+tree.apiDir, "CREATE_SQL="+createSQL, "SUPERSCHEMATIC_RULES_TEST_DATABASE_URL="+dsn)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("table_operations_runtime.test.ts failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range tableCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(tableCases) {
		t.Errorf("bun ran %d cases, and tableCases names %d:\n%s", len(cases), len(tableCases), junit)
	}
	if testing.Verbose() {
		t.Logf("table_operations_runtime.test.ts:\n%s", out)
	}
}

// TestTableRouteLiteralHoldsItsRuleAndItsStarts: the router's table writes
// a row rule's members and its override permission (D108), and the body
// members that name the state a row starts in (D109), only when the
// operation has them.
func TestTableRouteLiteralHoldsItsRuleAndItsStarts(t *testing.T) {
	plain := &TableRoute{Kind: "create", Type: "Task", Repository: "task", View: []string{"id", "status"}, Body: []string{"title", "status"}}
	ruled := *plain
	ruled.Starts, ruled.By, ruled.Override = []string{"status"}, []string{"ownerId", "createdBy"}, "tasks.admin"
	for _, tc := range []struct {
		name  string
		route *TableRoute
		want  []string
		not   []string
	}{
		{"a plain create", plain, []string{"kind: 'create'", "body: ['title', 'status']"}, []string{"starts:", "by:", "overridePermission:"}},
		{"a create with a rule and a start", &ruled, []string{"starts: ['status']", "by: ['ownerId', 'createdBy']", "overridePermission: 'tasks.admin'"}, nil},
	} {
		literal := tc.route.Literal()
		for _, want := range tc.want {
			if !strings.Contains(literal, want) {
				t.Errorf("%s: the literal lacks %s:\n%s", tc.name, want, literal)
			}
		}
		for _, not := range tc.not {
			if strings.Contains(literal, not) {
				t.Errorf("%s: the literal holds %s:\n%s", tc.name, not, literal)
			}
		}
	}
}
