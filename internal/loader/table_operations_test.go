package loader

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
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
		// A row rule is kept as the decorator says it (D108).
		"listNotes":  {Kind: "list", Database: db, Type: "Note", By: []string{"owner", "sharedWith"}, OverridePermission: "notes.admin"},
		"getNote":    {Kind: "read", Database: db, Type: "Note", Key: "id", By: []string{"owner", "sharedWith"}, OverridePermission: "notes.admin"},
		"createNote": {Kind: "create", Database: db, Type: "Note", Parents: issue, Input: "input", By: []string{"owner"}},
		"updateNote": {Kind: "update", Database: db, Type: "Note", Key: "id", Input: "input", By: []string{"owner"}},
		"deleteNote": {Kind: "delete", Database: db, Type: "Note", Key: "id", By: []string{"createdBy"}, OverridePermission: "notes.admin"},
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
		// Who created a row filters as any id does (D110).
		"createdBy": "uuid eq,in",
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
			[]string{"Issues.getIssue: @requireOwnership asks the implementation to check what the caller owns, and a @tableOperation has none: name the row's members that hold who it is for in @tableOperation({ by: [...] }) (D108), or write the operation by hand"}},
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
		// The row rule (D108).
		{"a row rule on a route with no caller", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("getIssueForGuest").TableOperation.By = []string{"createdBy"}
		}, []string{"Issues.getIssueForGuest: its row rule reaches the rows that name the caller, and the route admits a request with none; guard it with @auth or @requirePermission, or put it in an Authenticated set (D108)"}},
		{"an override permission with no rule", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("getIssue").TableOperation.OverridePermission = "issues.admin"
		}, []string{`Issues.getIssue: its @tableOperation names overridePermission "issues.admin", which lifts a row rule, and names no by; list the row's members that name who may reach it`}},
		{"a rule that names no member", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("getNote").TableOperation.By = []string{"user"}
		}, []string{"Notes.getNote: by names user, which is no member of Note; a row rule names a member that holds a subject (id, issueId, owner, sharedWith, text, createdBy)"}},
		{"a rule that names a member twice", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("getNote").TableOperation.By = []string{"owner", "owner"}
		}, []string{"Notes.getNote: by names owner twice"}},
		{"a rule that names a time", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("getNote").TableOperation.By = []string{"createdAt"}
		}, []string{"Notes.getNote: by names createdAt, which the repository stamps at a write and says nothing of who the row is for; name createdBy, or a member of the row (id, issueId, owner, sharedWith, text, createdBy)"}},
		{"a rule that names who last wrote the row", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("getIssue").TableOperation.By = []string{"updatedBy"}
		}, []string{"Issues.getIssue: by names updatedBy, which the repository stamps at a write and says nothing of who the row is for; name createdBy, or a member of the row (id, teamId, title, body, status, assigneeId, createdBy)"}},
		{"a rule that names a number", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("getIssue").TableOperation.By = []string{"priority"}
		}, []string{"Issues.getIssue: by names priority, which holds no subject a row rule compares the caller with: text, a UUID, a to-one relation's key or createdBy, none of them secret or a behavior's (id, teamId, title, body, status, assigneeId, createdBy)"}},
		{"a create whose rule names the key", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("createNote").TableOperation.By = []string{"id"}
		}, []string{
			"Notes.createNote: by names id, the key of Note, and a create's row rule sets each member it names to the caller: a create names no key",
			"Notes.createNote: its body CreateNoteInput leaves out owner, which Note requires and gives no default: no request could create a row; add it to the view, or name it in the path",
		}},
		{"a create whose rule names its path's relation", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("createNote").TableOperation.By = []string{"owner", "issueId"}
		}, []string{"Notes.createNote: by names issueId, which the path's {issueId} sets, and a create's row rule sets each member it names to the caller: one of them sets it"}},
		{"a create whose body sets a member of its rule", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("createNote").TableOperation.By = []string{"owner", "sharedWith"}
		}, []string{"Notes.createNote: its body CreateNoteInput sets sharedWith, the member its row rule sets to the caller; leave the field out of the view"}},
		{"a create with a rule of createdBy alone sets nothing", func(_ *ir.Schema, op func(string) *ir.FieldDef) {
			op("createIssue").TableOperation.By = []string{"createdBy"}
		}, nil},
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

// TestACreateNamesTheStateItsRowStartsIn: a create's body may hold the
// field of a Workflow of its table (D109), which the loader records as a
// start, the state the handler moves the new row to by the Workflow's
// transition; an update's body may not, and says where a row changes
// state; and a list of states is refused.
func TestACreateNamesTheStateItsRowStartsIn(t *testing.T) {
	loaded, tracker := loadTracker(t)
	// Issue's status as a Workflow's field, in a copy of the database.
	db := copyOf(t, tracker)
	issue := db.Types["Issue"]
	issue.Behaviors = append(issue.Behaviors, ir.BehaviorRef{Name: "Workflow", Field: "status",
		Config: json.RawMessage(`{"states":["open","started","done"],"transitions":[{"from":"open","to":"started"},{"from":"started","to":"done"}]}`)})
	status := ir.TypeRef{Name: "IssueStatus"}
	check := func(edit func(api *ir.Schema)) (*ir.Schema, []string) {
		api := copyOf(t, loaded)
		// The fixture's update sets status as a plain column's; without
		// it the API is sound over the Workflow.
		update := api.Types["UpdateIssueInput"]
		update.Fields = slices.DeleteFunc(update.Fields, func(f *ir.FieldDef) bool { return f.Name == "status" })
		if edit != nil {
			edit(api)
		}
		var got []string
		for _, d := range verify.CheckTableOperations(api, verify.Input{Database: db}).Errors {
			got = append(got, d.Error())
		}
		return api, got
	}

	api, errs := check(func(api *ir.Schema) {
		create := api.Types["CreateIssueInput"]
		create.Fields = append(create.Fields, &ir.FieldDef{Name: "status", TypeRef: status})
	})
	if len(errs) != 0 {
		t.Fatalf("a create whose body names the starting state: %q", errs)
	}
	if got := trackerOperation(t, api, "createIssue").TableOperation; !slices.Equal(got.Starts, []string{"status"}) || got.Kind != "create" {
		t.Errorf("createIssue's tableOperation = %+v, want its start status", got)
	}
	for _, name := range []string{"updateIssue", "getIssue", "listIssues", "deleteIssue", "createTeam"} {
		if got := trackerOperation(t, api, name).TableOperation; len(got.Starts) != 0 {
			t.Errorf("%s names starts %v", name, got.Starts)
		}
	}

	// Without the body's member a create starts in the initial state, and
	// names no start.
	api, errs = check(nil)
	if len(errs) != 0 || len(trackerOperation(t, api, "createIssue").TableOperation.Starts) != 0 {
		t.Errorf("a create that names no state: %q, %+v", errs, trackerOperation(t, api, "createIssue").TableOperation)
	}

	for _, tc := range []struct {
		name string
		edit func(api *ir.Schema)
		want []string
	}{
		{"an update that sets the state", func(api *ir.Schema) {
			update := api.Types["UpdateIssueInput"]
			update.Fields = append(update.Fields, &ir.FieldDef{Name: "status", TypeRef: status})
		}, []string{"Issues.updateIssue: its body UpdateIssueInput sets status, the state of a Workflow, which a row changes through its transition (issue.status.transition); only a create's body names the state a row starts in; leave the field out of the view"}},
		{"a create that lists states", func(api *ir.Schema) {
			create := api.Types["CreateIssueInput"]
			create.Fields = append(create.Fields, &ir.FieldDef{Name: "status", TypeRef: ir.TypeRef{Name: "IssueStatus", IsArray: true}})
		}, []string{"Issues.createIssue: its body CreateIssueInput holds status as IssueStatus[]; the state a row starts in is one state of Issue.status"}},
	} {
		if _, got := check(tc.edit); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// TestTableOperationsDocumentThemselves: a @tableOperation that declares
// no @docs and no @mcp is given both from its declaration (D111): a title
// from its method's name, a description from its comment, a capability
// from its set and its method, and a visible tool under its name; an
// operation the project writes is given neither.
func TestTableOperationsDocumentThemselves(t *testing.T) {
	api, db := loadTracker(t)
	type documented struct {
		title, description, capability, replay, handle, invocation string
	}
	of := func(schema *ir.Schema, name string) documented {
		op := trackerOperation(t, schema, name)
		var d documented
		if op.Docs != nil {
			d.title, d.description, d.capability, d.replay = op.Docs.Title, op.Docs.Description, op.Docs.Capability, string(op.Docs.ReplayMode)
			if err := ir.ValidateOperationDocs(op.Docs); err != nil {
				t.Errorf("%s: its docs are not valid: %v", name, err)
			}
		}
		if op.MCP != nil {
			d.handle, d.invocation = op.MCP.Handle, op.MCP.Invocation.Key+"="+op.MCP.Invocation.Value
			if op.MCP.Hidden {
				d.handle = "hidden: " + op.MCP.HiddenReason
			}
		}
		return d
	}
	for name, want := range map[string]documented{
		"listTeams":        {"List teams", "The teams, a page at a time.", "teams.list-teams", "read_only", "list_teams", "invocationPolicy=auto"},
		"getTeam":          {"Get team", "One team.", "teams.get-team", "read_only", "get_team", "invocationPolicy=auto"},
		"createTeam":       {"Create team", "A new team; a key another team holds is a conflict.", "teams.create-team", "", "create_team", "invocationPolicy=auto"},
		"getIssueForGuest": {"Get issue for guest", "One issue as anyone sees it: another view of the same table behind another guard.", "issues.get-issue-for-guest", "read_only", "get_issue_for_guest", "invocationPolicy=auto"},
		"updateIssue":      {"Update issue", "Changes the members the body sets.", "issues.update-issue", "", "update_issue", "invocationPolicy=auto"},
		// A @mcp of its own, and no @docs: the tool is the author's, and
		// the docs are still given.
		"deleteNote": {"Delete note", "Deletes a note the caller wrote. It names its own tool and no @docs, which its declaration gives it (D111).", "notes.delete-note", "", "remove_note", "invocationPolicy=auto"},
		// An operation the project writes says its own.
		"summarizeTeam": {},
	} {
		if got := of(api, name); got != want {
			t.Errorf("%s:\n got %+v\nwant %+v", name, got, want)
		}
		if docs := trackerOperation(t, api, name).Docs; docs != nil && (docs.Lifecycle != ir.DocsLifecycleActive || docs.Visibility != ir.DocsVisibilityPublic || docs.MappingStatus != ir.DocsMappingStatusMapped) {
			t.Errorf("%s: docs %+v, want active, public and mapped", name, docs)
		}
	}

	// bare is a copy with every table operation's docs and tool taken off,
	// as the reader left them, for a case to edit and the pass to fill.
	bare := func(edit func(schema *ir.Schema, op func(string) *ir.FieldDef)) *ir.Schema {
		schema := copyOf(t, api)
		for _, set := range schema.OperationSets {
			for _, op := range set.Operations {
				if op.TableOperation != nil {
					op.Docs, op.MCP = nil, nil
				}
			}
		}
		edit(schema, func(name string) *ir.FieldDef { return trackerOperation(t, schema, name) })
		if errs := verify.CheckTableOperations(schema, verify.Input{Database: db}).Errors; len(errs) != 0 {
			t.Fatalf("the edited API does not check: %v", errs)
		}
		return schema
	}

	// A method with no comment says what its kind does to its table.
	schema := bare(func(_ *ir.Schema, op func(string) *ir.FieldDef) {
		for _, name := range []string{"listTeams", "getTeam", "createTeam", "updateIssue", "deleteIssue"} {
			op(name).Comment = ""
		}
	})
	for name, want := range map[string]string{
		"listTeams":   "Lists the Team rows, a page at a time.",
		"getTeam":     "Reads the Team its key names.",
		"createTeam":  "Creates a Team.",
		"updateIssue": "Updates the Issue its key names: a member left out stays as it is.",
		"deleteIssue": "Deletes the Issue its key names.",
	} {
		if got := of(schema, name).description; got != want {
			t.Errorf("%s with no comment: %q, want %q", name, got, want)
		}
	}

	// What an operation declares is kept: its @docs, its @mcp, a hidden one
	// among them. The other is still given.
	schema = bare(func(_ *ir.Schema, op func(string) *ir.FieldDef) {
		op("getTeam").Docs = &ir.OperationDocs{Title: "A team", Description: "Reads a team.", Capability: "teams.read", Lifecycle: ir.DocsLifecycleDraft, Visibility: ir.DocsVisibilityInternal, MappingStatus: ir.DocsMappingStatusMapped}
		op("createTeam").MCP = &ir.OperationMCP{Handle: "new_team", Invocation: ir.MCPInvocation{Key: "invocationPolicy", Value: "ask"}}
		op("deleteIssue").MCP = &ir.OperationMCP{Hidden: true, HiddenReason: "Deleting is a person's to do."}
	})
	for name, want := range map[string]documented{
		"getTeam":     {"A team", "Reads a team.", "teams.read", "", "get_team", "invocationPolicy=auto"},
		"createTeam":  {"Create team", "A new team; a key another team holds is a conflict.", "teams.create-team", "", "new_team", "invocationPolicy=ask"},
		"deleteIssue": {"Delete issue", "Deletes the issue, which is kept, and answers it as it was.", "issues.delete-issue", "", "hidden: Deleting is a person's to do.", "="},
	} {
		if got := of(schema, name); got != want {
			t.Errorf("%s, declared in part:\n got %+v\nwant %+v", name, got, want)
		}
	}
	if docs := trackerOperation(t, schema, "getTeam").Docs; docs.Lifecycle != ir.DocsLifecycleDraft || docs.Visibility != ir.DocsVisibilityInternal {
		t.Errorf("getTeam's own docs were rewritten: %+v", docs)
	}

	// A name two sets share, or a handle or a title another operation
	// declares, takes its set's before it, so the API's tools stay apart.
	schema = bare(func(_ *ir.Schema, op func(string) *ir.FieldDef) {
		op("listMembers").Name = "listTeams"
		op("getIssue").MCP = &ir.OperationMCP{Handle: "get_team", Invocation: ir.MCPInvocation{Key: "invocationPolicy", Value: "auto"}}
		op("createIssue").Docs = &ir.OperationDocs{Title: "Create member", Description: "x", Capability: "issues.create", Lifecycle: ir.DocsLifecycleActive, Visibility: ir.DocsVisibilityPublic, MappingStatus: ir.DocsMappingStatusMapped}
	})
	named := map[string][2]string{}
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			if op.MCP != nil && op.Docs != nil {
				named[set.Name+"."+op.Name] = [2]string{op.MCP.Handle, op.Docs.Title}
			}
		}
	}
	for owner, want := range map[string][2]string{
		"Teams.listTeams":      {"teams_list_teams", "Teams: list teams"},
		"Members.listTeams":    {"members_list_teams", "Members: list teams"},
		"Teams.getTeam":        {"teams_get_team", "Teams: get team"},
		"Members.createMember": {"members_create_member", "Members: create member"},
		"Teams.createTeam":     {"create_team", "Create team"},
	} {
		if named[owner] != want {
			t.Errorf("%s: handle and title %q, want %q", owner, named[owner], want)
		}
	}
	seen := map[string]string{}
	for owner, name := range named {
		for _, key := range []string{"handle " + name[0], "title " + strings.ToLower(name[1])} {
			if other, twice := seen[key]; twice {
				t.Errorf("%s and %s share the %s", owner, other, key)
			}
			seen[key] = owner
		}
	}

	// An operation only a calling service reaches is no tool of an end
	// user's agent, and neither is one whose payload is encrypted.
	schema = bare(func(schema *ir.Schema, op func(string) *ir.FieldDef) {
		op("createMember").ServiceCallers = &ir.ServiceCallers{Mode: ir.ServiceCallersRequire, From: []string{"billing"}}
		op("createTeam").Encrypted = true
	})
	if got := of(schema, "createMember").handle; got != "hidden: Only the services that @requireService admits call it (D37)." {
		t.Errorf("a service's operation: %q", got)
	}
	if got := of(schema, "createTeam").handle; got != "hidden: Its payload is encrypted, which only the SDK seals." {
		t.Errorf("an encrypted operation: %q", got)
	}
}
