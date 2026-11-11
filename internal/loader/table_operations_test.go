package loader

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

const trackerAPI = "fixture-tracker-api"

// loadTracker loads fixture-tracker-api and its database.
func loadTracker(t *testing.T) (api, db *ir.Schema) {
	t.Helper()
	services := filepath.Join("tsreader", "testdata", "services")
	api, err := LoadService(filepath.Join(services, trackerAPI))
	if err != nil {
		t.Fatalf("LoadService(%s): %v", trackerAPI, err)
	}
	db, err = LoadService(filepath.Join(services, "fixture-tracker-db"))
	if err != nil {
		t.Fatalf("LoadService(fixture-tracker-db): %v", err)
	}
	return api, db
}

func trackerOperation(t *testing.T, schema *ir.Schema, name string) *ir.FieldDef {
	t.Helper()
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

// TestTableOperationsAreFilledFromTheirDeclarations: the loader decides
// what each @tableOperation does from its HTTP method and shape, finds its
// table from its views, and reads its path's parameters as the key and the
// row's parents (D106); an operation without the decorator has none.
func TestTableOperationsAreFilledFromTheirDeclarations(t *testing.T) {
	api, _ := loadTracker(t)
	const db = "fixture-tracker-db"
	team := []ir.TableOperationParent{{Param: "teamId", Relation: "team"}}
	issue := []ir.TableOperationParent{{Param: "issueId", Relation: "issue"}}
	want := map[string]*ir.TableOperation{
		"listTeams":        {Kind: "list", Database: db, Type: "Team"},
		"getTeam":          {Kind: "read", Database: db, Type: "Team", Key: "id"},
		"createTeam":       {Kind: "create", Database: db, Type: "Team", Input: "input"},
		"summarizeTeam":    nil,
		"listMembers":      {Kind: "list", Database: db, Type: "Member"},
		"createMember":     {Kind: "create", Database: db, Type: "Member", Input: "input"},
		"listIssues":       {Kind: "list", Database: db, Type: "Issue", Parents: team},
		"getIssue":         {Kind: "read", Database: db, Type: "Issue", Key: "id"},
		"getIssueForGuest": {Kind: "read", Database: db, Type: "Issue", Key: "id"},
		"createIssue":      {Kind: "create", Database: db, Type: "Issue", Parents: team, Input: "input"},
		"updateIssue":      {Kind: "update", Database: db, Type: "Issue", Key: "id", Input: "input"},
		"deleteIssue":      {Kind: "delete", Database: db, Type: "Issue", Key: "id"},
		"listComments":     {Kind: "list", Database: db, Type: "Comment", Parents: issue},
		"createComment":    {Kind: "create", Database: db, Type: "Comment", Parents: issue, Input: "input"},
		"deleteComment":    {Kind: "delete", Database: db, Type: "Comment", Key: "id", Parents: issue},
	}
	seen := 0
	for _, set := range api.OperationSets {
		for _, op := range set.Operations {
			expected, ok := want[op.Name]
			if !ok {
				t.Errorf("%s.%s: an operation the test does not expect", set.Name, op.Name)
				continue
			}
			seen++
			if !reflect.DeepEqual(op.TableOperation, expected) {
				t.Errorf("%s.%s: tableOperation %+v, want %+v", set.Name, op.Name, op.TableOperation, expected)
			}
		}
	}
	if seen != len(want) {
		t.Errorf("%d operations, want %d", seen, len(want))
	}

	// The list's filter is held to the view's columns, each with its kind.
	filter := trackerOperation(t, api, "listIssues").Filter
	if filter == nil || filter.Argument != "filter" {
		t.Fatalf("listIssues filter = %+v", filter)
	}
	kinds := map[string]string{}
	for _, field := range filter.Fields {
		kinds[field.Name] = field.Kind + " " + strings.Join(field.Operators, ",")
	}
	wantKinds := map[string]string{
		"status":     "enum eq,in",
		"priority":   "int eq,in,gt,gte,lt,lte",
		"title":      "string eq,in,contains",
		"estimate":   "float eq,in,gt,gte,lt,lte,isNull",
		"dueOn":      "date eq,in,gt,gte,lt,lte,isNull",
		"assigneeId": "uuid eq,in,isNull",
		"createdAt":  "datetime eq,in,gt,gte,lt,lte",
	}
	if !reflect.DeepEqual(kinds, wantKinds) {
		t.Errorf("listIssues filter kinds = %v, want %v", kinds, wantKinds)
	}
}

// copyOf is a deep copy of a loaded schema, which a case may edit.
func copyOf(t *testing.T, schema *ir.Schema) *ir.Schema {
	t.Helper()
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var out ir.Schema
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

// TestTableOperationsAreHeldToTheirTable holds a @tableOperation to its
// database, its views, its HTTP method and shape, its path and its guards,
// one case per message (D106).
func TestTableOperationsAreHeldToTheirTable(t *testing.T) {
	loaded, db := loadTracker(t)
	field := func(schema *ir.Schema, typeName, name string) *ir.FieldDef {
		for _, f := range schema.Types[typeName].Fields {
			if f.Name == name {
				return f
			}
		}
		t.Fatalf("%s has no field %s", typeName, name)
		return nil
	}
	uuid := ir.TypeRef{Name: "Identity.UUID"}
	for _, test := range []struct {
		name string
		edit func(schema *ir.Schema, op func(string) *ir.FieldDef)
		want []string
	}{
		{"@requireOwnership", func(_ *ir.Schema, op func(string) *ir.FieldDef) { op("getIssue").RequireOwnership = true },
			[]string{"Issues.getIssue: @requireOwnership asks the implementation to check what the caller owns, and a @tableOperation has none: write the operation by hand, or guard the route another way"}},
		{"@manualRouteRegistration", func(_ *ir.Schema, op func(string) *ir.FieldDef) { op("getIssue").ManualRouteRegistration = true },
			[]string{"Issues.getIssue: @manualRouteRegistration hands the route to a handler of yours, and @tableOperation to one the build writes: an operation takes one of them"}},
		{"a plain result", func(_ *ir.Schema, op func(string) *ir.FieldDef) { op("getTeam").TypeRef.Name = "TeamSummary" },
			[]string{"Teams.getTeam: its result TeamSummary is a plain type; @tableOperation reads and writes @source views of a table of fixture-tracker-db, and writes no handler for anything else"}},
		{"a result that is no type", func(_ *ir.Schema, op func(string) *ir.FieldDef) { op("getTeam").TypeRef.Name = "string" },
			[]string{"Teams.getTeam: its result is string, which is no type of fixture-tracker-api; @tableOperation serves @source views of a table of fixture-tracker-db"}},
		{"a view of another service", func(schema *ir.Schema, _ func(string) *ir.FieldDef) {
			schema.Types["TeamView"].Source.Target = "other-db.Team"
		},
			[]string{
				"Teams.listTeams: its result TeamView is @source(Team), a type of other-db, not of fixture-tracker-db, the API's database (its authDb, or its one DB dependency)",
				"Teams.getTeam: its result TeamView is @source(Team), a type of other-db, not of fixture-tracker-db, the API's database (its authDb, or its one DB dependency)",
				"Teams.createTeam: its result TeamView is @source(Team), a type of other-db, not of fixture-tracker-db, the API's database (its authDb, or its one DB dependency)",
			}},
		{"views of two tables", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("createTeam").Arguments[0].TypeRef.Name = "CreateMemberInput"
		},
			[]string{"Teams.createTeam: its body CreateMemberInput is @source(Member) and its result TeamView is @source(Team); a @tableOperation reads and writes one table"}},
		{"a plain body", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("createTeam").Arguments[0].TypeRef.Name = "TeamSummary"
		},
			[]string{"Teams.createTeam: its body TeamSummary is a plain type; @tableOperation reads and writes @source views of a table of fixture-tracker-db, and writes no handler for anything else"}},
		{"a PUT", func(_ *ir.Schema, op func(string) *ir.FieldDef) { op("updateIssue").HTTPMethod = "PUT" },
			[]string{"Issues.updateIssue: a PUT is refused on a @tableOperation: it would replace the row, or upsert it by a key the caller names, and neither is built (D106); update the row with a PATCH, or write the operation by hand"}},
		{"a GET of one view without the key", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("getTeam").RestPath = "team"
			op("getTeam").Arguments = nil
		}, []string{"Teams.getTeam: a GET whose result is one view reads a row of Team, and its path holds no {id}, the table's key; a GET that lists rows answers TeamView[]"}},
		{"a GET of a list with the key", func(_ *ir.Schema, op func(string) *ir.FieldDef) { op("getTeam").TypeRef.IsArray = true },
			[]string{"Teams.getTeam: a GET whose result is a list of views lists the rows of Team, and its path holds {id}, one row's key; a GET that reads one row answers one TeamView"}},
		{"a POST with the key", func(_ *ir.Schema, op func(string) *ir.FieldDef) { op("updateIssue").HTTPMethod = "POST" },
			[]string{"Issues.updateIssue: a POST creates a row of Issue, and its path holds {id}, a row's key; an update of that row is a PATCH"}},
		{"a PATCH without the key", func(_ *ir.Schema, op func(string) *ir.FieldDef) { op("createTeam").HTTPMethod = "PATCH" },
			[]string{"Teams.createTeam: a PATCH updates a row of Team, and its path holds no {id}, the table's key"}},
		{"a DELETE without the key", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("deleteIssue").RestPath = "issues"
			op("deleteIssue").Arguments = nil
		}, []string{"Issues.deleteIssue: a DELETE deletes a row of Issue, and its path holds no {id}, the table's key"}},
		{"a DELETE with a body", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("deleteIssue").Arguments = append(op("deleteIssue").Arguments, &ir.ArgumentDef{Name: "reason", TypeRef: ir.TypeRef{Name: "string"}, Required: true})
		}, []string{"Issues.deleteIssue: argument reason is a body argument, and a delete takes none"}},
		{"a nullable result", func(_ *ir.Schema, op func(string) *ir.FieldDef) { op("getIssue").Required = false },
			[]string{"Issues.getIssue: its result is Nullable; a read answers the row's view, and 404 when the key holds no live row"}},
		{"a path parameter that is no relation's key", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("getIssue").RestPath = "orgs/{orgId}/issues/{id}"
			op("getIssue").Arguments = append(op("getIssue").Arguments, &ir.ArgumentDef{Name: "orgId", TypeRef: uuid, Required: true})
		}, []string{"Issues.getIssue: path parameter orgId names neither Issue's key, id, nor the key of one of its relations (teamId, assigneeId); a path's parameter holds the row to its parent, and nothing says what orgId compares"}},
		{"a parent of another type", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("listIssues").Arguments[0].TypeRef.Name = "string"
		},
			[]string{"Issues.listIssues: path parameter teamId holds string, which is not the type of Team's key, the row Issue.team relates to"}},
		{"a key of another type", func(_ *ir.Schema, op func(string) *ir.FieldDef) { op("getIssue").Arguments[0].TypeRef.Name = "string" },
			[]string{"Issues.getIssue: path parameter id holds string, and Issue's key id holds Identity.UUID"}},
		{"a QueryParam on a list", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("listTeams").Arguments = []*ir.ArgumentDef{{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, IsQuery: true}}
		}, []string{"Teams.listTeams: argument name is a QueryParam, and nothing says what a generated list compares it with; filter by a field with FilterBy, or write the list by hand"}},
		{"a list that is not paged", func(_ *ir.Schema, op func(string) *ir.FieldDef) { op("listTeams").Page = nil },
			[]string{"Teams.listTeams: a generated list is @paginated: it reads a page of Team's rows, never the whole table"}},
		{"a search of a table with no search field", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("listTeams").Arguments = []*ir.ArgumentDef{{Name: "q", TypeRef: ir.TypeRef{Name: "string"}, IsQuery: true, Search: true}}
		}, []string{"Teams.listTeams: argument q is a SearchQuery, and Team has no @searchField for it to search"}},
		{"a sort on a read", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("getTeam").Arguments = append(op("getTeam").Arguments, &ir.ArgumentDef{Name: "sort", TypeRef: ir.TypeRef{Name: "string"}, IsQuery: true, Sort: []string{"name"}})
		}, []string{"Teams.getTeam: argument sort is a SortBy, which orders a list; a read has none"}},
		{"a virtual field of the result", func(schema *ir.Schema, _ func(string) *ir.FieldDef) {
			view := schema.Types["IssueGuestView"]
			view.Fields = append(view.Fields, &ir.FieldDef{Name: "commentCount", TypeRef: ir.TypeRef{Name: "number"}, Required: true, Virtual: true})
		}, []string{"Issues.getIssueForGuest: its result IssueGuestView has the @virtual field commentCount, which no column of Issue holds and no implementation of a @tableOperation fills; leave the field out of the view, or write the operation by hand"}},
		{"a create that sets the generated key", func(schema *ir.Schema, _ func(string) *ir.FieldDef) {
			view := schema.Types["CreateTeamInput"]
			view.Fields = append(view.Fields, &ir.FieldDef{Name: "id", TypeRef: uuid, Required: true})
		}, []string{"Teams.createTeam: its body CreateTeamInput sets id, the key, which Team generates; leave the field out of the view"}},
		{"a create that sets an audit field", func(schema *ir.Schema, _ func(string) *ir.FieldDef) {
			view := schema.Types["CreateIssueInput"]
			view.Fields = append(view.Fields, &ir.FieldDef{Name: "createdBy", TypeRef: uuid})
		}, []string{"Issues.createIssue: its body CreateIssueInput sets createdBy, an audit field, which the repository stamps; leave the field out of the view"}},
		{"a create that sets its path's relation", func(schema *ir.Schema, _ func(string) *ir.FieldDef) {
			view := schema.Types["CreateIssueInput"]
			view.Fields = append(view.Fields, &ir.FieldDef{Name: "teamId", TypeRef: uuid, Required: true})
		}, []string{"Issues.createIssue: its body CreateIssueInput sets teamId, the relation the path's {teamId} sets; leave the field out of the view"}},
		{"a create that leaves out a required column", func(schema *ir.Schema, _ func(string) *ir.FieldDef) {
			view := schema.Types["CreateTeamInput"]
			view.Fields = view.Fields[:1]
		}, []string{"Teams.createTeam: its body CreateTeamInput leaves out name, which Team requires and gives no default: no request could create a row; add it to the view, or name it in the path"}},
		{"a create with no parent in its path", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("createIssue").RestPath = "issues"
			op("createIssue").Arguments = op("createIssue").Arguments[1:]
		}, []string{"Issues.createIssue: its body CreateIssueInput leaves out teamId, which Issue requires and gives no default: no request could create a row; add it to the view, or name it in the path"}},
		{"an update with a required member", func(schema *ir.Schema, _ func(string) *ir.FieldDef) {
			field(schema, "UpdateIssueInput", "title").Required = true
		},
			[]string{"Issues.updateIssue: its body UpdateIssueInput requires title; an update's members are all optional, each one left out leaving its column as it is: write title: Nullable<…>"}},
		{"an update that sets the key", func(schema *ir.Schema, _ func(string) *ir.FieldDef) {
			view := schema.Types["UpdateIssueInput"]
			view.Fields = append(view.Fields, &ir.FieldDef{Name: "id", TypeRef: uuid})
		}, []string{"Issues.updateIssue: its body UpdateIssueInput sets id, the key, which Issue generates; leave the field out of the view"}},
		{"two bodies", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("createTeam").Arguments = append(op("createTeam").Arguments, &ir.ArgumentDef{Name: "note", TypeRef: ir.TypeRef{Name: "string"}, Required: true})
		}, []string{"Teams.createTeam: a create takes one body argument, a @source view of Team, beside its path's parameters, and this takes input, note"}},
		{"members written otherwise", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("getTeam").TableOperation = &ir.TableOperation{Kind: "list", Database: "fixture-tracker-db", Type: "Team"}
		},
			[]string{"Teams.getTeam: its tableOperation says a list of fixture-tracker-db.Team, and its declaration makes it a read of fixture-tracker-db.Team; leave the members out, the loader fills them"}},
	} {
		schema := copyOf(t, loaded)
		test.edit(schema, func(name string) *ir.FieldDef { return trackerOperation(t, schema, name) })
		res := verify.CheckTableOperations(schema, verify.Input{Database: db})
		var got []string
		for _, d := range res.Errors {
			got = append(got, d.Error())
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Errorf("%s:\n got %q\nwant %q", test.name, got, test.want)
		}
	}

	// Without a database, or without the loader having read it.
	schema := copyOf(t, loaded)
	schema.Dependencies = nil
	res := verify.CheckTableOperations(schema, verify.Input{})
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Error(), "connects to none: set its authDb, or give it one DB dependency") {
		t.Errorf("an API with no database: %v", res.Errors)
	}
	res = verify.CheckTableOperations(copyOf(t, loaded), verify.Input{})
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Error(), "which the loader could not read") {
		t.Errorf("a database the loader did not read: %v", res.Errors)
	}
}
