package apigen_test

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
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// The table operations fixture (D106): fixture-tracker-api serves the
// tables of fixture-tracker-db, its one DB dependency, from methods marked
// @tableOperation, beside one operation the project writes,
// Teams.summarizeTeam.
var trackerFixtures = behaviorFixtures{"fixture-tracker-api", "fixture-tracker-db"}

// TestTableOperationEndpoints: an operation marked @tableOperation is an
// endpoint as any other for the documents, with the loader's record
// (D106), and the Go server's view leaves it out of what the project
// implements: the namespaces, the interface, the scaffolds and the
// implementation scaffold hold summarizeTeam alone, the router mounts the
// rest on handlers over the ORM, and tables.go is modelled from them.
func TestTableOperationEndpoints(t *testing.T) {
	api, db := loadFixturePair(t, trackerFixtures)
	output := generatePairAPI(t, trackerFixtures, api, db)

	var kinds []string
	for _, ep := range output.Endpoints {
		if to := ep.TableOperation(); to != nil {
			kinds = append(kinds, ep.Method+" "+ep.Path+" "+to.Kind+" "+to.Type+" "+ep.TableFunc())
		}
	}
	want := []string{
		"DELETE /api/issues/{id} delete Issue tableIssuesDeleteIssue",
		"GET /api/issues/{id} read Issue tableIssuesGetIssue",
		"PATCH /api/issues/{id} update Issue tableIssuesUpdateIssue",
		"GET /api/issues/{issueId}/comments list Comment tableCommentsListComments",
		"POST /api/issues/{issueId}/comments create Comment tableCommentsCreateComment",
		"DELETE /api/issues/{issueId}/comments/{id} delete Comment tableCommentsDeleteComment",
		"POST /api/issues/{issueId}/notes create Note tableNotesCreateNote",
		"GET /api/members list Member tableMembersListMembers",
		"POST /api/members create Member tableMembersCreateMember",
		"GET /api/notes list Note tableNotesListNotes",
		"DELETE /api/notes/{id} delete Note tableNotesDeleteNote",
		"GET /api/notes/{id} read Note tableNotesGetNote",
		"PATCH /api/notes/{id} update Note tableNotesUpdateNote",
		"GET /api/public/issues/{id} read Issue tableIssuesGetIssueForGuest",
		"GET /api/teams list Team tableTeamsListTeams",
		"POST /api/teams create Team tableTeamsCreateTeam",
		"GET /api/teams/{id} read Team tableTeamsGetTeam",
		"GET /api/teams/{teamId}/issues list Issue tableIssuesListIssues",
		"POST /api/teams/{teamId}/issues create Issue tableIssuesCreateIssue",
	}
	if !slices.Equal(kinds, want) {
		t.Fatalf("table endpoints =\n%s\nwant\n%s", strings.Join(kinds, "\n"), strings.Join(want, "\n"))
	}
	if !output.HasTableEndpoints() || !output.TakesDatabase() || output.HasBehaviorEndpoints() {
		t.Fatalf("HasTableEndpoints %t, TakesDatabase %t, HasBehaviorEndpoints %t", output.HasTableEndpoints(), output.TakesDatabase(), output.HasBehaviorEndpoints())
	}
	// Deps hold the database's ORM and its Go types, which tables.go reads
	// and writes the rows of; none of its tables composes a behavior.
	if deps := output.Deps; deps.Database != trackerFixtures.db || deps.ORMModule != "example.com/schemas/orm/fixture-tracker-db" || deps.Rules ||
		deps.DatabaseTypes != "example.com/schemas/types/go/fixture-tracker-db" || deps.DatabaseTypesAlias != "fixturetrackerdb" {
		t.Fatalf("Deps = %+v", deps)
	}
	if !slices.Contains(output.IndirectModules, output.Deps.DatabaseTypes) {
		t.Errorf("go.mod does not reach %s: %v", output.Deps.DatabaseTypes, output.IndirectModules)
	}
	// Each documents itself (D111): a title and a tool from its method,
	// which the OpenAPI document and the tool documents then carry; the
	// operation the project writes declares neither.
	for _, ep := range output.Endpoints {
		switch ep.Name {
		case "listIssues":
			if ep.Docs == nil || ep.Docs.Title != "List issues" || ep.Docs.Capability != "issues.list-issues" || ep.MCP == nil || ep.MCP.Hidden || ep.MCP.Handle != "list_issues" || ep.MCP.Name != "List issues" || ep.MCP.Invocation.Value != "auto" {
				t.Errorf("listIssues: docs %+v, mcp %+v", ep.Docs, ep.MCP)
			}
		case "summarizeTeam":
			if ep.Docs != nil || ep.MCP != nil {
				t.Errorf("summarizeTeam, which the project writes: docs %+v, mcp %+v", ep.Docs, ep.MCP)
			}
		}
	}
	if !strings.Contains(output.OpenAPISpecRaw, `"summary": "List issues"`) || !strings.Contains(output.OpenAPISpecRaw, `"summary": "Delete note"`) {
		t.Error("the OpenAPI document lacks the table operations' summaries")
	}
	// The documents hold every operation.
	if len(output.Endpoints) != len(want)+1 || !strings.Contains(output.OpenAPISpecRaw, `"/api/teams/{teamId}/issues"`) || !strings.Contains(output.PermissionCatalogJSON, "issues.write") {
		t.Errorf("the output holds %d endpoints", len(output.Endpoints))
	}

	view, err := apigen.ImplementedOutput(output)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(view.Namespaces, []string{"teams"}) || len(view.Endpoints) != 1 || view.Endpoints[0].Name != "summarizeTeam" || len(view.TableEndpoints) != len(want) {
		t.Fatalf("the implemented view has namespaces %v, %d endpoints and %d table endpoints, want [teams], summarizeTeam alone and %d", view.Namespaces, len(view.Endpoints), len(view.TableEndpoints), len(want))
	}
	if len(view.HandlerEndpoints()) != len(want)+1 || len(view.RoutedEndpoints()) != len(want)+1 {
		t.Errorf("the router has %d handlers and %d routes, want %d of each", len(view.HandlerEndpoints()), len(view.RoutedEndpoints()), len(want)+1)
	}
	// The router's flags count the table operations' routes; the
	// interface's imports only what the project implements.
	if !view.HasAuth || !view.HasPermissionEndpoints || !view.RoutesNeedPaging() || !view.RoutesNeedFilters() || view.HasPagedEndpoints() || view.HasFilteredEndpoints {
		t.Errorf("flags: HasAuth %t, HasPermissionEndpoints %t, RoutesNeedPaging %t, RoutesNeedFilters %t, HasPagedEndpoints %t, HasFilteredEndpoints %t",
			view.HasAuth, view.HasPermissionEndpoints, view.RoutesNeedPaging(), view.RoutesNeedFilters(), view.HasPagedEndpoints(), view.HasFilteredEndpoints)
	}
	tables := view.Tables
	if tables == nil || tables.Database != trackerFixtures.db || len(tables.Handlers) != len(want) || len(tables.Filters) != 2 {
		t.Fatalf("Tables = %+v", tables)
	}
	var views, rows []string
	for _, v := range tables.Views {
		views = append(views, v.Func+" "+v.RowType)
	}
	for _, r := range tables.Rows {
		rows = append(rows, r.Var)
	}
	if !slices.Equal(views, []string{"asCommentView fixturetrackerdb.Comment", "asIssueGuestView fixturetrackerdb.Issue", "asIssueView fixturetrackerdb.Issue", "asMemberView fixturetrackerdb.Member", "asNoteView fixturetrackerdb.Note", "asTeamView fixturetrackerdb.Team"}) ||
		!slices.Equal(rows, []string{"rowsOfComment", "rowsOfIssue", "rowsOfMember", "rowsOfNote", "rowsOfTeam"}) {
		t.Errorf("views %v, rows %v", views, rows)
	}
	// The output Generate returned is as it was.
	if output.Tables != nil || output.TableEndpoints != nil || len(output.Endpoints) != len(want)+1 {
		t.Error("ImplementedOutput changed the output it was given")
	}

	// What the project implements, in each file written for it.
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	if _, err := apigen.WriteScaffolds(output, filepath.Join(outDir, "scaffolds")); err != nil {
		t.Fatalf("write scaffolds: %v", err)
	}
	if _, err := apigen.WriteImplementationScaffold(output, filepath.Join(outDir, "impl")); err != nil {
		t.Fatalf("write implementation scaffold: %v", err)
	}
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	methods := func(source, receiver string) []string {
		var names []string
		for _, line := range strings.Split(source, "\n") {
			line = strings.TrimSpace(line)
			if name, _, ok := strings.Cut(strings.TrimPrefix(line, receiver), "(ctx context.Context"); ok && strings.HasPrefix(line, receiver) {
				names = append(names, name)
			}
		}
		return names
	}
	interfaces := read("interfaces.go")
	if got := methods(interfaces, ""); !slices.Equal(got, []string{"SummarizeTeam"}) || strings.Count(interfaces, "Implementation interface {") != 1 || strings.Contains(interfaces, "Filter struct") {
		t.Errorf("interfaces.go declares the methods %v:\n%s", got, interfaces)
	}
	if got := methods(read("impl/implementation.go"), "func (impl *Teams) "); !slices.Equal(got, []string{"SummarizeTeam"}) || strings.Contains(read("impl/implementation.go"), "Issues") {
		t.Errorf("the implementation scaffold declares the methods %v", got)
	}
	var scaffolds []string
	if err := filepath.WalkDir(filepath.Join(outDir, "scaffolds"), func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasSuffix(path, ".go") {
			rel, _ := filepath.Rel(filepath.Join(outDir, "scaffolds"), path)
			scaffolds = append(scaffolds, filepath.ToSlash(rel))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(scaffolds)
	if !slices.Contains(scaffolds, "teams/summarize_team.go") || slices.ContainsFunc(scaffolds, func(name string) bool {
		return strings.HasPrefix(name, "issues/") || strings.HasPrefix(name, "members/") || strings.HasPrefix(name, "comments/") || strings.Contains(name, "list_teams") || strings.Contains(name, "create_team") || strings.Contains(name, "get_team")
	}) {
		t.Errorf("the scaffolds are %v, want summarizeTeam's alone", scaffolds)
	}
	routes := read("routes.go")
	for _, want := range []string{
		"func createIssuesUpdateIssueHandler(db orm.DatabaseInterface) gohttp.HandlerFunc {",
		"result, err := tableIssuesUpdateIssue(r.Context(), db, Id, &input, rawInput)",
		"result, err := tableIssuesListIssues(r.Context(), db, TeamId, Q, filters, page)",
		"respondTableError(w, r, err)",
		"Handler: createIssuesGetIssueForGuestHandler(cfg.DB),",
		// A route that requires a caller and no permission, in an API
		// whose Config takes no auth middleware, checks for one itself.
		"Handler: requireTableCaller(createTeamsListTeamsHandler(cfg.DB)),",
		"Handler: createTeamsCreateTeamHandler(cfg.DB),",
		"Handler: createTeamsSummarizeTeamHandler(cfg.Implementations.Teams),",
		`return fmt.Errorf("Config.DB is required: it serves the table operations of fixture-tracker-api (D106)")`,
	} {
		if !strings.Contains(routes, want) {
			t.Errorf("routes.go holds no %q", want)
		}
	}
	if strings.Contains(routes, "impl.ListIssues") || strings.Contains(routes, "cfg.Implementations.Issues") {
		t.Error("routes.go calls an implementation of a table operation")
	}
}

// TestWriteAPIGoldenTableOperations generates the Go API module of
// fixture-tracker-api and holds the files the table operations change to
// their golden copies (D106): tables.go, the handlers; the routes, mounted
// on them; the interfaces and the implementation scaffold, which hold
// summarizeTeam alone; deps.go; and go.mod, which requires pgx, whose
// refusals tables.go reads. Regenerate with:
// go test ./internal/generator/apigen -run TestWriteAPIGoldenTableOperations -update
func TestWriteAPIGoldenTableOperations(t *testing.T) {
	api, db := loadFixturePair(t, trackerFixtures)
	output := generatePairAPI(t, trackerFixtures, api, db)
	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+trackerFixtures.api); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	if _, err := apigen.WriteImplementationScaffold(output, filepath.Join(outDir, "impl")); err != nil {
		t.Fatalf("write implementation scaffold: %v", err)
	}
	checkGoldenFiles(t, outDir, filepath.Join("testdata", "golden", trackerFixtures.api), []string{
		"go.mod", "tables.go", "routes.go", "interfaces.go", "deps.go", "impl/implementation.go",
	})
}

// TestTableOperationsAPICompiles generates fixture-tracker-db's Go types
// and ORM and fixture-tracker-api's Go types and API module, with its
// scaffolds and its implementation scaffold, into a temporary tree shaped
// as the dist, wired to the checkout's runtime modules, and builds and vets
// the API module: tables.go fills each view from the ORM's row, and each
// row and update from a body, with the types typegen and ormgen declare.
// The generator package's TestTableOperationsGoServerServesItsTables runs
// the module against Postgres.
func TestTableOperationsAPICompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	api, db := loadFixturePair(t, trackerFixtures)
	// A filter may name the member the path's parent holds: the parent
	// holds beside it, not under it.
	filter := tableOperation(t, api, "listIssues").Filter
	filter.Fields = append(filter.Fields, &ir.FilterField{Name: "teamId", Kind: ir.FilterKindUUID, Operators: ir.FilterOperators(ir.FilterKindUUID, false), TypeRef: &ir.TypeRef{Name: "Identity.UUID"}})
	apiDir, output := writeFixtureModulesWith(t, trackerFixtures, api, db, nil)
	if _, err := apigen.WriteScaffolds(output, filepath.Join(apiDir, "scaffolds")); err != nil {
		t.Fatal(err)
	}
	if _, err := apigen.WriteImplementationScaffold(output, filepath.Join(apiDir, "impl")); err != nil {
		t.Fatal(err)
	}
	buildGoModule(t, apiDir)
	filtered, err := os.ReadFile(filepath.Join(apiDir, "tables.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"where := &orm.IssueFilter{TeamID: &orm.UUIDFilter{Eq: &TeamId}}",
		"where.AllOf = append(where.AllOf, orm.IssueFilter{TeamID: &orm.UUIDFilter{Eq: filter.Eq, Ne: filter.Ne, In: filter.In, NotIn: filter.NotIn}})",
		"where.AssigneeID = &orm.UUIDFilter{Eq: filter.Eq, Ne: filter.Ne, In: filter.In, NotIn: filter.NotIn, IsNull: filter.IsNull}",
	} {
		if !strings.Contains(string(filtered), want) {
			t.Errorf("tables.go holds no %q", want)
		}
	}

	// An API that only reads its tables, with no filter, search or sort,
	// holds none of what a write and a filter need, and builds too.
	api, db = loadFixturePair(t, trackerFixtures)
	api.OperationSets = slices.DeleteFunc(api.OperationSets, func(set *ir.OperationSet) bool { return set.Name != "Teams" })
	teams := api.OperationSets[0]
	teams.Operations = slices.DeleteFunc(teams.Operations, func(op *ir.FieldDef) bool { return op.Name == "createTeam" })
	apiDir, _ = writeFixtureModulesWith(t, trackerFixtures, api, db, nil)
	buildGoModule(t, apiDir)
	tables, err := os.ReadFile(filepath.Join(apiDir, "tables.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"pgconn", "tableWriteError", "tableDeleteError", "tableMembers", "tableText", "filterparse"} {
		if strings.Contains(string(tables), absent) {
			t.Errorf("tables.go of an API that only reads holds %s", absent)
		}
	}
}

// TestTableOperationsBesideUnservedOnesCompile builds and vets
// fixture-tracker-api with table operations the Go server serves beside
// ones it does not yet (D106): Issue holds a list IssueView shows, so
// every operation of IssueView is the implementation's, with its filter's
// type and its page, in the interface, the scaffolds and the
// implementation scaffold, while the guest's view of the same table and
// the other tables are served from tables.go. An API none of whose table
// operations is served builds too, with no tables.go and no database in
// its Config.
func TestTableOperationsBesideUnservedOnesCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	build := func(t *testing.T, api, db *ir.Schema) string {
		t.Helper()
		apiDir, output := writeFixtureModulesWith(t, trackerFixtures, api, db, nil)
		if _, err := apigen.WriteScaffolds(output, filepath.Join(apiDir, "scaffolds")); err != nil {
			t.Fatal(err)
		}
		if _, err := apigen.WriteImplementationScaffold(output, filepath.Join(apiDir, "impl")); err != nil {
			t.Fatal(err)
		}
		buildGoModule(t, apiDir)
		return apiDir
	}
	read := func(t *testing.T, dir, name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	api, db := loadFixturePair(t, trackerFixtures)
	withLabels(api, db)
	apiDir := build(t, api, db)
	interfaces, tables, impl := read(t, apiDir, "interfaces.go"), read(t, apiDir, "tables.go"), read(t, apiDir, "impl/implementation.go")
	for _, want := range []string{
		"ListIssues(ctx context.Context, teamId types.IdentityUUID, q *string, filter IssuesListIssuesFilter, page paging.Request) (paging.Page[types.IssueView], error)",
		"UpdateIssue(ctx context.Context, id types.IdentityUUID, input *types.UpdateIssueInput) (*types.IssueView, error)",
		"type IssuesListIssuesFilter struct {",
	} {
		if !strings.Contains(interfaces, want) {
			t.Errorf("interfaces.go holds no %q", want)
		}
	}
	for _, want := range []string{"func tableIssuesGetIssueForGuest(", "func asIssueGuestView(row *fixturetrackerdb.Issue) types.IssueGuestView", "func tableCommentsCreateComment(", "func tableTeamsListTeams("} {
		if !strings.Contains(tables, want) {
			t.Errorf("tables.go holds no %q", want)
		}
	}
	for _, absent := range []string{"asIssueView", "tableIssuesListIssues", "IssuesListIssuesFilter", "filterparse", "tableContains"} {
		if strings.Contains(tables, absent) {
			t.Errorf("tables.go holds %s, of an operation the implementation serves", absent)
		}
	}
	if !strings.Contains(impl, "func (impl *Issues) ListIssues(") || strings.Contains(impl, "GetIssueForGuest") {
		t.Errorf("the implementation scaffold:\n%s", impl)
	}
	if _, err := os.Stat(filepath.Join(apiDir, "scaffolds", "issues", "update_issue.go")); err != nil {
		t.Errorf("no scaffold of updateIssue: %v", err)
	}

	api, db = loadFixturePair(t, trackerFixtures)
	for _, set := range api.OperationSets {
		for _, op := range set.Operations {
			if op.TableOperation != nil {
				op.TableOperation.Projection = true
			}
		}
	}
	apiDir = build(t, api, db)
	if _, err := os.Stat(filepath.Join(apiDir, "tables.go")); err == nil {
		t.Error("an API that serves no table operation holds tables.go")
	}
	if mod := read(t, apiDir, "go.mod"); strings.Contains(read(t, apiDir, "routes.go"), "cfg.DB") {
		t.Errorf("an API that serves no table operation takes the database:\n%s", mod)
	}
}

// buildGoModule runs go mod tidy, go build and go vet in a generated
// module.
func buildGoModule(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{{"mod", "tidy"}, {"build", "./..."}, {"vet", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

// tableOperation finds an operation of the API by its name.
func tableOperation(t *testing.T, api *ir.Schema, name string) *ir.FieldDef {
	t.Helper()
	for _, set := range api.OperationSets {
		for _, op := range set.Operations {
			if op.Name == name {
				return op
			}
		}
	}
	t.Fatalf("the API has no operation %s", name)
	return nil
}

// withLabels gives fixture-tracker-db's Issue a list of labels, which
// IssueView shows: a view the Go handlers do not copy member by member.
func withLabels(api, db *ir.Schema) {
	labels := func() *ir.FieldDef {
		return &ir.FieldDef{Name: "labels", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, Required: true}
	}
	db.Types["Issue"].Fields = append(db.Types["Issue"].Fields, labels())
	api.Types["IssueView"].Fields = append(api.Types["IssueView"].Fields, labels())
}

// TestATableOperationTheGoServerDoesNotServeYetStaysImplemented: a table
// operation the loader filled and the Go handlers cannot serve yet, a
// projection's, one whose view holds a list, and a create that leaves a
// column to a default no Go literal writes, is no error (D106's last
// rule): it stays an operation the project implements, in the interface,
// the scaffold and the routes, with no handler in tables.go, and
// UnservedTableOperations names it and says why, for the build's warning.
// The table operations beside it are served as before.
func TestATableOperationTheGoServerDoesNotServeYetStaysImplemented(t *testing.T) {
	const (
		projection = "its result IssueView reads the projection Issue, and the Go server writes handlers over a table's repository alone so far"
		list       = "its view IssueView holds labels, string[], and the Go server writes handlers for a view of single values so far, each a scalar, text, a number, a boolean or an enum"
		owner      = "a create of Issue leaves ownerId to its default, 00000000-0000-4000-8000-000000000000, which the Go server cannot write as a value of Identity.UUID"
	)
	for name, tc := range map[string]struct {
		edit func(api, db *ir.Schema)
		want []apigen.UnservedTableOperation
		// methods are the implementation's, by its interface, and served
		// how many operations tables.go serves.
		methods []string
		served  int
	}{
		"a projection": {
			func(api, _ *ir.Schema) { tableOperation(t, api, "getIssue").TableOperation.Projection = true },
			[]apigen.UnservedTableOperation{{Operation: "Issues.getIssue", Reason: projection}},
			[]string{"GetIssue", "SummarizeTeam"}, 18,
		},
		"a view with a list": {
			withLabels,
			[]apigen.UnservedTableOperation{
				{Operation: "Issues.deleteIssue", Reason: list}, {Operation: "Issues.getIssue", Reason: list}, {Operation: "Issues.updateIssue", Reason: list},
				{Operation: "Issues.listIssues", Reason: list}, {Operation: "Issues.createIssue", Reason: list},
			},
			[]string{"DeleteIssue", "GetIssue", "UpdateIssue", "ListIssues", "CreateIssue", "SummarizeTeam"}, 14,
		},
		"a default no literal writes": {
			func(_, db *ir.Schema) {
				zero := "00000000-0000-4000-8000-000000000000"
				db.Types["Issue"].Fields = append(db.Types["Issue"].Fields, &ir.FieldDef{Name: "ownerId", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Default: &zero})
			},
			[]apigen.UnservedTableOperation{{Operation: "Issues.createIssue", Reason: owner}},
			[]string{"CreateIssue", "SummarizeTeam"}, 18,
		},
		// A create that names the state its row starts in (D109) makes
		// a Workflow's transition, which the Go handlers do not yet.
		"a create that starts its row in a state": {
			func(api, _ *ir.Schema) {
				tableOperation(t, api, "createIssue").TableOperation.Starts = []string{"status"}
			},
			[]apigen.UnservedTableOperation{{Operation: "Issues.createIssue", Reason: "its body names the state the row starts in, status, and the Go server writes no create that makes a Workflow's transition so far (D109)"}},
			[]string{"CreateIssue", "SummarizeTeam"}, 18,
		},
		"every operation": {
			func(api, _ *ir.Schema) {
				for _, set := range api.OperationSets {
					for _, op := range set.Operations {
						if op.TableOperation != nil {
							op.TableOperation.Projection = true
						}
					}
				}
			},
			nil, nil, 0,
		},
	} {
		t.Run(name, func(t *testing.T) {
			api, db := loadFixturePair(t, trackerFixtures)
			tc.edit(api, db)
			output := generatePairAPI(t, trackerFixtures, api, db)
			unserved, err := apigen.UnservedTableOperations(output)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want != nil && !slices.Equal(unserved, tc.want) {
				t.Errorf("UnservedTableOperations =\n%+v\nwant\n%+v", unserved, tc.want)
			}
			if tc.want == nil && len(unserved) != 19 {
				t.Errorf("UnservedTableOperations lists %d operations, want all 19", len(unserved))
			}
			if got := len(output.ServedTableEndpoints()); got != tc.served || output.HasTableEndpoints() != (tc.served > 0) || output.TakesDatabase() != (tc.served > 0) {
				t.Errorf("the output serves %d table operations (HasTableEndpoints %t), want %d", got, output.HasTableEndpoints(), tc.served)
			}

			view, err := apigen.ImplementedOutput(output)
			if err != nil {
				t.Fatal(err)
			}
			if len(view.TableEndpoints) != tc.served || len(view.Endpoints) != 20-tc.served || (view.Tables != nil) != (tc.served > 0) {
				t.Fatalf("the implemented view has %d table endpoints and %d endpoints, want %d and %d", len(view.TableEndpoints), len(view.Endpoints), tc.served, 20-tc.served)
			}
			for _, ep := range view.Endpoints {
				if (ep.TableOperation() != nil) != ep.TableByHand || ep.ServedTable() != nil {
					t.Errorf("%s: TableByHand %t, ServedTable %v", ep.Name, ep.TableByHand, ep.ServedTable())
				}
			}
			if view.HasTableEndpoints() != (tc.served > 0) {
				t.Errorf("the view's HasTableEndpoints = %t", view.HasTableEndpoints())
			}
			// The output every other writer reads is as it was.
			if len(output.Endpoints) != 20 || output.Tables != nil || slices.ContainsFunc(output.Endpoints, func(ep apigen.EndpointInfo) bool { return ep.TableByHand }) {
				t.Error("ImplementedOutput changed the output it was given")
			}

			outDir := t.TempDir()
			if err := apigen.WriteAPI(output, outDir); err != nil {
				t.Fatalf("write api: %v", err)
			}
			interfaces, err := os.ReadFile(filepath.Join(outDir, "interfaces.go"))
			if err != nil {
				t.Fatal(err)
			}
			routes, err := os.ReadFile(filepath.Join(outDir, "routes.go"))
			if err != nil {
				t.Fatal(err)
			}
			tables, err := os.ReadFile(filepath.Join(outDir, "tables.go"))
			if (err == nil) != (tc.served > 0) {
				t.Fatalf("tables.go: %v, with %d served operations", err, tc.served)
			}
			for _, method := range tc.methods {
				if !strings.Contains(string(interfaces), "\t"+method+"(ctx context.Context") {
					t.Errorf("interfaces.go has no method %s", method)
				}
				if method == "SummarizeTeam" {
					continue
				}
				if !strings.Contains(string(routes), "impl."+method+"(") || !strings.Contains(string(routes), "Handler(impl IssuesImplementation) gohttp.HandlerFunc {") {
					t.Errorf("routes.go does not call the implementation's %s", method)
				}
				if strings.Contains(string(tables), "func tableIssues"+method+"(") || strings.Contains(string(routes), "tableIssues"+method+"(") {
					t.Errorf("tables.go serves %s, which the implementation does", method)
				}
			}
			if tc.methods != nil && strings.Count(string(interfaces), "(ctx context.Context") != len(tc.methods) {
				t.Errorf("interfaces.go declares other methods than %v:\n%s", tc.methods, interfaces)
			}
			if tc.served == 0 && (strings.Contains(string(routes), "cfg.DB") || strings.Contains(string(routes), "requireTableCaller")) {
				t.Error("routes.go of an API that serves no table operation takes the database")
			}
		})
	}
}

// TestATableOperationTheLoaderDidNotFillIsAnError: IR the loader did not
// fill is an error of the Go server's writers that names the operation
// (D106), and of UnservedTableOperations; the output every other writer
// reads is generated all the same.
func TestATableOperationTheLoaderDidNotFillIsAnError(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(api, db *ir.Schema)
		want string
	}{
		"no kind": {
			func(api, _ *ir.Schema) { tableOperation(t, api, "getIssue").TableOperation.Kind = "" },
			"operation Issues.getIssue: its @tableOperation has no kind, table and database; the loader fills them",
		},
		"the decorator's empty record": {
			func(api, _ *ir.Schema) { tableOperation(t, api, "createTeam").TableOperation = &ir.TableOperation{} },
			"operation Teams.createTeam: its @tableOperation has no kind, table and database; the loader fills them",
		},
		"a kind that is none": {
			func(api, _ *ir.Schema) { tableOperation(t, api, "getIssue").TableOperation.Kind = "upsert" },
			"operation Issues.getIssue: its @tableOperation has no kind, table and database",
		},
		"a table the database lacks": {
			func(api, _ *ir.Schema) { tableOperation(t, api, "getIssue").TableOperation.Type = "Ticket" },
			"operation Issues.getIssue: its @tableOperation names Ticket, which is no table of fixture-tracker-db with a repository",
		},
		"a read with no key": {
			func(api, _ *ir.Schema) { tableOperation(t, api, "getIssue").TableOperation.Key = "" },
			"operation Issues.getIssue: path parameter id names neither the key of Issue nor the key of one of its relations",
		},
		"a parent that is no relation": {
			func(api, _ *ir.Schema) {
				tableOperation(t, api, "listIssues").TableOperation.Parents = []ir.TableOperationParent{{Param: "teamId", Relation: "squad"}}
			},
			"operation Issues.listIssues: its @tableOperation holds the row to squad by {teamId}, which is no key of a relation of Issue",
		},
		"a path parameter the record leaves out": {
			func(api, _ *ir.Schema) { tableOperation(t, api, "createIssue").TableOperation.Parents = nil },
			"operation Issues.createIssue: path parameter teamId names neither the key of Issue nor the key of one of its relations",
		},
		"a list that does not page": {
			func(api, _ *ir.Schema) { tableOperation(t, api, "listTeams").Page = nil },
			"operation Teams.listTeams: a generated list is a @paginated GET",
		},
		"a result with a field no column holds": {
			func(api, _ *ir.Schema) {
				api.Types["TeamView"].Fields = append(api.Types["TeamView"].Fields, &ir.FieldDef{Name: "size", TypeRef: ir.TypeRef{Name: "number"}, Required: true})
			},
			"its result TeamView holds size, which no column of Team holds",
		},
		"a body that requires a member of an update": {
			func(api, _ *ir.Schema) { api.Types["UpdateIssueInput"].Fields[0].Required = true },
			"operation Issues.updateIssue: its body UpdateIssueInput requires title; an update's members are all optional",
		},
		"a body that sets an audit field": {
			func(api, _ *ir.Schema) {
				api.Types["CreateIssueInput"].Fields = append(api.Types["CreateIssueInput"].Fields, &ir.FieldDef{Name: "createdBy", TypeRef: ir.TypeRef{Name: "Identity.UUID"}})
			},
			"operation Issues.createIssue: its body CreateIssueInput sets createdBy, which is no column of Issue a caller may set",
		},
	} {
		t.Run(name, func(t *testing.T) {
			api, db := loadFixturePair(t, trackerFixtures)
			tc.edit(api, db)
			output := generatePairAPI(t, trackerFixtures, api, db)
			_, err := apigen.ImplementedOutput(output)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ImplementedOutput = %v, want %q", err, tc.want)
			}
			if err := apigen.WriteAPI(output, t.TempDir()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("WriteAPI = %v, want %q", err, tc.want)
			}
			if _, err := apigen.UnservedTableOperations(output); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("UnservedTableOperations = %v, want %q", err, tc.want)
			}
		})
	}

	// Without the database's schema the Go server cannot model its rows.
	api, _ := loadFixturePair(t, trackerFixtures)
	output, err := apigen.Generate(api, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  trackerFixtures.api,
		ModulePath:  "example.com/schemas/api/" + trackerFixtures.api,
		TypesModule: "example.com/schemas/types/go/" + trackerFixtures.api,
	})
	if err != nil {
		t.Fatal(err)
	}
	const want = "its @tableOperation reads and writes a table of fixture-tracker-db, whose schema the generator was not given (Options.Dependencies)"
	if _, err := apigen.ImplementedOutput(output); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("ImplementedOutput without the database = %v, want %q", err, want)
	}
}

// TestTableOperationsOverATableOfBehaviorsCompile serves fixture-workflow-db's
// Order, whose status is a Workflow, through table operations added to
// fixture-workflow-api beside its behavior operations, as the loader fills
// them, and builds and vets the API module (D106, D103): a create fills
// the ORM's OrderCreate, which leaves the status to the core; every
// operation runs as the caller the core checks; and a refusal of the
// core's is answered as a behavior operation's is.
func TestTableOperationsOverATableOfBehaviorsCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	withTables := func(t *testing.T) (*ir.Schema, *ir.Schema) {
		t.Helper()
		api, db := loadBehaviorFixtures(t)
		source := &ir.SourceRef{Target: behaviorDB + ".Order"}
		uuid, text, at := ir.TypeRef{Name: "Identity.UUID"}, ir.TypeRef{Name: "string"}, ir.TypeRef{Name: "Temporal.DateTime"}
		// The views hold a scalar the API did not import until now.
		api.Scalars[at.Name] = db.Scalars[at.Name]
		api.Types["OrderRow"] = &ir.TypeDef{Name: "OrderRow", Role: ir.RoleAPIView, Source: source, Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: uuid, Required: true}, {Name: "title", TypeRef: text, Required: true}, {Name: "placedAt", TypeRef: at},
		}}
		api.Types["CreateOrderInput"] = &ir.TypeDef{Name: "CreateOrderInput", Role: ir.RoleAPIView, Source: source, Fields: []*ir.FieldDef{
			{Name: "title", TypeRef: text, Required: true}, {Name: "placedAt", TypeRef: at},
		}}
		api.Types["UpdateOrderInput"] = &ir.TypeDef{Name: "UpdateOrderInput", Role: ir.RoleAPIView, Source: source, Fields: []*ir.FieldDef{
			{Name: "title", TypeRef: text}, {Name: "placedAt", TypeRef: at},
		}}
		id := &ir.ArgumentDef{Name: "id", TypeRef: uuid, Required: true}
		body := func(name string) *ir.ArgumentDef {
			return &ir.ArgumentDef{Name: "input", TypeRef: ir.TypeRef{Name: name}, Required: true}
		}
		row := ir.TypeRef{Name: "OrderRow"}
		api.OperationSets = append(api.OperationSets, &ir.OperationSet{Name: "OrderRows", Operations: []*ir.FieldDef{
			{Name: "listOrderRows", TypeRef: ir.TypeRef{Name: "OrderRow", IsArray: true}, Required: true, Auth: true, HTTPMethod: "GET", RestPath: "order-rows", Page: &ir.OperationPage{DefaultLimit: 20, MaxLimit: 100}, TableOperation: &ir.TableOperation{}},
			{Name: "getOrderRow", TypeRef: row, Required: true, Auth: true, HTTPMethod: "GET", RestPath: "order-rows/{id}", Arguments: []*ir.ArgumentDef{id}, TableOperation: &ir.TableOperation{}},
			{Name: "createOrderRow", TypeRef: row, Required: true, Auth: true, HTTPMethod: "POST", RestPath: "order-rows", Arguments: []*ir.ArgumentDef{body("CreateOrderInput")}, TableOperation: &ir.TableOperation{}},
			{Name: "updateOrderRow", TypeRef: row, Required: true, Auth: true, HTTPMethod: "PATCH", RestPath: "order-rows/{id}", Arguments: []*ir.ArgumentDef{id, body("UpdateOrderInput")}, TableOperation: &ir.TableOperation{}},
			{Name: "deleteOrderRow", TypeRef: row, Required: true, Auth: true, HTTPMethod: "DELETE", RestPath: "order-rows/{id}", Arguments: []*ir.ArgumentDef{id}, TableOperation: &ir.TableOperation{}},
		}})
		if result := verify.CheckTableOperations(api, verify.Input{Database: db}); len(result.Errors) > 0 {
			t.Fatalf("the loader refuses the table operations: %v", result.Errors)
		}
		return api, db
	}

	api, db := withTables(t)
	apiDir := writeBehaviorModules(t, api, db)
	buildGoModule(t, apiDir)
	tables, err := os.ReadFile(filepath.Join(apiDir, "tables.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"row := &orm.OrderCreate{}",
		"ctx = orm.WithBehaviorCaller(callerContext(ctx), behaviorCaller(ctx))",
		"if _, refused := rules.AsRefusal(err); refused {",
		"respondBehaviorError(w, r, err)",
	} {
		if !strings.Contains(string(tables), want) {
			t.Errorf("tables.go holds no %q", want)
		}
	}
	// behaviors.go declares the behaviors' caller once.
	if strings.Contains(string(tables), "func behaviorCaller(") {
		t.Error("tables.go declares behaviorCaller beside behaviors.go")
	}

	// Without a behavior operation, tables.go holds the caller and the
	// refusal's answer itself.
	api, db = withTables(t)
	api.OperationSets = slices.DeleteFunc(api.OperationSets, func(set *ir.OperationSet) bool { return set.IsBehaviorOperations() })
	apiDir = writeBehaviorModules(t, api, db)
	buildGoModule(t, apiDir)
	if tables, err = os.ReadFile(filepath.Join(apiDir, "tables.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(apiDir, "behaviors.go")); err == nil {
		t.Error("an API without a behavior operation holds behaviors.go")
	}
	for _, want := range []string{"func behaviorCaller(ctx context.Context) rules.Caller {", "if refusal, ok := rules.AsRefusal(err); ok && refusal.Status >= 400"} {
		if !strings.Contains(string(tables), want) {
			t.Errorf("tables.go holds no %q", want)
		}
	}
}
