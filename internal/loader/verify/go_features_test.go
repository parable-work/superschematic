package verify

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// goFeatureFindings are checkGoFeatures' findings of schema under in.
func goFeatureFindings(schema *ir.Schema, in Input) []string {
	r := &Result{}
	checkGoFeatures(schema, in, r)
	var out []string
	for _, err := range r.Errors {
		out = append(out, err.Error())
	}
	return out
}

// TestGoServerDatabaseErrRefusesTheDatabasesFeatures: the generator's
// check of a Go server's database (D180) names the feature and the table,
// and passes a database with none.
func TestGoServerDatabaseErrRefusesTheDatabasesFeatures(t *testing.T) {
	api := ir.NewSchema("app-api", ir.SchemaKindAPI)
	db := ir.NewSchema("app-db", ir.SchemaKindDB)
	db.Types["Ticket"] = &ir.TypeDef{Name: "Ticket", Role: ir.RoleDBTable}
	if err := GoServerDatabaseErr(api, db); err != nil {
		t.Fatalf("a plain database: %v", err)
	}
	db.Types["Ticket"].Party = &ir.PartyDef{}
	if err := GoServerDatabaseErr(api, db); err == nil || !strings.Contains(err.Error(), "the Go server of app-api, over app-db, serves @party on Ticket through the notifier's host (D190, D192), and the database declares no @notifier(\"dispatch\") queue") {
		t.Fatalf("a party table with no notifier: %v", err)
	}
	db.Types["NoticeWork"] = &ir.TypeDef{Name: "NoticeWork", Role: ir.RoleDBTable, Notifier: &ir.NotifierDef{Part: ir.NotifierDispatch}}
	if err := GoServerDatabaseErr(api, db); err != nil {
		t.Fatalf("a party table with the notifier: %v", err)
	}
}

// TestGoOutputsRefuseThePlatformFeaturesGoDoesNotServe: a Go server over a
// database refuses each feature of D152 to D159 of its tables that Go does
// not serve yet, naming the feature, the table and the decision (D180).
// The database itself, whose Go types and Go ORM the build always
// generates, holds no finding, nor does an API served in TypeScript or an
// audience, which Go serves.
func TestGoOutputsRefuseThePlatformFeaturesGoDoesNotServe(t *testing.T) {
	table := func(name string, edit func(*ir.TypeDef)) *ir.Schema {
		schema := ir.NewSchema("app-db", ir.SchemaKindDB)
		td := &ir.TypeDef{Name: name, Role: ir.RoleDBTable, Owner: "src/app.schema.ts"}
		edit(td)
		schema.Types[name] = td
		return schema
	}
	// Parties, conversations, run requests and agents are served (D192),
	// through the notifier's host, which a database with none of the
	// notifier's tables has not.
	for _, tc := range []struct {
		feature string
		edit    func(*ir.TypeDef)
	}{
		{"@party", func(td *ir.TypeDef) { td.Party = &ir.PartyDef{} }},
		{"@conversations", func(td *ir.TypeDef) { td.Conversations = &ir.ConversationsDef{} }},
		{"@runRequests", func(td *ir.TypeDef) { td.RunRequests = &ir.RunRequestsDef{} }},
		{"@agents", func(td *ir.TypeDef) { td.Agents = &ir.AgentsDef{} }},
	} {
		t.Run(tc.feature, func(t *testing.T) {
			schema := table("Duty", tc.edit)
			// The database's own Go ORM is its plain repositories.
			if got := goFeatureFindings(schema, Input{GoServer: true}); len(got) != 0 {
				t.Fatalf("the database: %v", got)
			}
			api := ir.NewSchema("app-api", ir.SchemaKindAPI)
			got := goFeatureFindings(api, Input{GoServer: true, Database: schema})
			if want := "the Go server of app-api, over app-db, serves " + tc.feature + " on Duty through the notifier's host"; len(got) != 1 || !strings.Contains(got[0], want) {
				t.Fatalf("the Go server: %v, want %q", got, want)
			}
			schema.Types["NoticeWork"] = &ir.TypeDef{Name: "NoticeWork", Role: ir.RoleDBTable, Notifier: &ir.NotifierDef{Part: ir.NotifierDispatch}}
			if got := goFeatureFindings(api, Input{GoServer: true, Database: schema}); len(got) != 0 {
				t.Fatalf("the Go server with the notifier: %v", got)
			}
			// An API served in TypeScript serves it.
			if got := goFeatureFindings(api, Input{Database: schema}); len(got) != 0 {
				t.Fatalf("a TypeScript server: %v", got)
			}
		})
	}

	// Per-record access is served (D180), references and attachments
	// (D184), Recurrence, the calendar and search (D187), and events, the
	// notifier and channel identities (D190).
	served := table("Document", func(td *ir.TypeDef) {
		td.Audience = &ir.AudienceDef{}
		td.RowReferences = &ir.RowReferencesDef{}
		td.Attachments = &ir.AttachmentsDef{Released: "FileReleased"}
		td.CalendarSources = []ir.CalendarSourceDef{{Field: "dueOn", Title: "title"}}
		td.Behaviors = []ir.BehaviorRef{{Name: "Recurrence", Implements: true}}
		td.Searchable = &ir.SearchableDef{}
		td.Events = []*ir.EventDef{{Kind: "document.filed"}}
		td.Fields = []*ir.FieldDef{{Name: "title", TypeRef: ir.TypeRef{Name: "string"}}}
	})
	served.Types["Grant"] = &ir.TypeDef{Name: "Grant", Role: ir.RoleDBTable, AccessGrants: &ir.AccessGrantsDef{}}
	served.Types["AccessEntry"] = &ir.TypeDef{Name: "AccessEntry", Role: ir.RoleDBTable, AccessLog: &ir.AccessLogDef{}}
	served.Types["Notice"] = &ir.TypeDef{Name: "Notice", Role: ir.RoleDBTable, Notifier: &ir.NotifierDef{Part: ir.NotifierInbox}}
	served.Types["ChannelIdentity"] = &ir.TypeDef{Name: "ChannelIdentity", Role: ir.RoleDBTable, ChannelIdentities: &ir.ChannelIdentitiesDef{}}
	if got := goFeatureFindings(ir.NewSchema("app-api", ir.SchemaKindAPI), Input{GoServer: true, Database: served}); len(got) != 0 {
		t.Fatalf("an audience, grants, the access log, references, attachments, Recurrence, the calendar, search, events, the notifier and channel identities in Go: %v", got)
	}
	// A calendar source titled by a sealed field is not: the Go calendar
	// reads a title from its column.
	served.Types["Document"].Fields[0].EncryptedAtRest = true
	if got := goFeatureFindings(ir.NewSchema("app-api", ir.SchemaKindAPI), Input{GoServer: true, Database: served}); len(got) != 1 || !strings.Contains(got[0], "does not serve @calendarSource document titled by title, an Encrypted<T> field (D152, D187)") {
		t.Fatalf("a sealed title: %v", got)
	}

	// An API's provider slots are served, over providers of Go modules
	// (D184); a provider of none, and an operation's autonomy, are not.
	api := ir.NewSchema("app-api", ir.SchemaKindAPI)
	api.Types["AppConfig"] = &ir.TypeDef{Name: "AppConfig", Owner: "src/config.ts", ProviderSlots: map[string]string{"SMS": "channel"}}
	api.Providers = []*ir.RuntimeProvider{{Name: "relay-a", Implements: "channel", Path: "providers/relay-a", Module: "example.com/providers/relay-a"}}
	if got := goFeatureFindings(api, Input{GoServer: true}); len(got) != 0 {
		t.Fatalf("an API's slots over a Go provider: %v", got)
	}
	api.Providers = append(api.Providers, &ir.RuntimeProvider{Name: "relay-b", Implements: "channel", Path: "providers/relay-b"})
	api.OperationSets = []*ir.OperationSet{{Name: "Duties", Operations: []*ir.FieldDef{{Name: "closeDuty", Autonomy: &ir.OperationAutonomy{}}}}}
	got := goFeatureFindings(api, Input{GoServer: true})
	if len(got) != 2 || !strings.Contains(got[0], "the Go server of app-api cannot link runtime provider relay-b (providers/relay-b): its directory holds no Go module") || !strings.Contains(got[1], "does not serve @autonomy on Duties.closeDuty yet (D158, D183, D192): it holds an agent run's calls of the API's MCP tools, and a Go server serves no MCP endpoint") {
		t.Fatalf("a provider of no Go module and autonomy: %v", got)
	}
}

// TestGoServerSQLiteErrRefusesRecurrenceAndTheCalendar: a Go server whose
// database lists sqlite first is refused Recurrence and a calendar source,
// each by its table, which it serves on Postgres alone (D187); one that
// lists postgres first, or a database with neither, is not.
func TestGoServerSQLiteErrRefusesRecurrenceAndTheCalendar(t *testing.T) {
	api := ir.NewSchema("app-api", ir.SchemaKindAPI)
	db := ir.NewSchema("app-db", ir.SchemaKindDB)
	db.Types["Filing"] = &ir.TypeDef{Name: "Filing", Role: ir.RoleDBTable, Behaviors: []ir.BehaviorRef{{Name: "Recurrence", Implements: true}}, CalendarSources: []ir.CalendarSourceDef{{Occurrences: true}}}
	db.Types["Note"] = &ir.TypeDef{Name: "Note", Role: ir.RoleDBTable, Searchable: &ir.SearchableDef{}}
	err := GoServerSQLiteErr(api, db, []string{"sqlite", "postgres"})
	if err == nil || !strings.Contains(err.Error(), "lists sqlite first, and serves Filing: Recurrence, Filing: @calendarSource, Note: @searchable on Postgres alone so far (D152, D153, D155, D157, D158, D187, D190, D192)") {
		t.Fatalf("sqlite first: %v", err)
	}
	// The parties' and agents' tables are Postgres's alone too (D192).
	db.Types["Contact"] = &ir.TypeDef{Name: "Contact", Role: ir.RoleDBTable, Party: &ir.PartyDef{}}
	db.Types["Run"] = &ir.TypeDef{Name: "Run", Role: ir.RoleDBTable, Agents: &ir.AgentsDef{Part: ir.AgentRuns}}
	if err := GoServerSQLiteErr(api, db, []string{"sqlite"}); err == nil || !strings.Contains(err.Error(), "Contact: @party, Filing: Recurrence, Filing: @calendarSource, Note: @searchable, Run: @agents") {
		t.Fatalf("parties and agents on sqlite: %v", err)
	}
	delete(db.Types, "Contact")
	delete(db.Types, "Run")
	if err := GoServerSQLiteErr(api, db, []string{"postgres", "sqlite"}); err != nil {
		t.Fatalf("postgres first: %v", err)
	}
	delete(db.Types, "Filing")
	delete(db.Types, "Note")
	if err := GoServerSQLiteErr(api, db, []string{"sqlite"}); err != nil {
		t.Fatalf("a database with none: %v", err)
	}
}

// TestGoServerSQLiteErrRefusesSearch: a Go server over a database that
// lists sqlite first and has a @searchable table, and nothing else the
// refusal names, is refused search, which the crosstable package runs by
// Postgres statements alone (D155, D187), now that the loader lets the
// database's Go types declare it on SQLite (D186); over a database that
// lists postgres first it is served.
// TestGoServerSQLiteErrRefusesTheNotifier: a Go server over a database
// that lists sqlite first is refused a table's events, notifier and
// channel identities, which the Go notifier reads by Postgres's statements
// alone (D190); the loader serves them on SQLite in TypeScript (D189), so
// this is the Go server's own refusal. Postgres first is not refused.
func TestGoServerSQLiteErrRefusesTheNotifier(t *testing.T) {
	api := ir.NewSchema("app-api", ir.SchemaKindAPI)
	db := ir.NewSchema("app-db", ir.SchemaKindDB)
	db.Types["Alert"] = &ir.TypeDef{Name: "Alert", Role: ir.RoleDBTable, Events: []*ir.EventDef{{Kind: "alert.raised"}}, Notifier: &ir.NotifierDef{}}
	db.Types["Identity"] = &ir.TypeDef{Name: "Identity", Role: ir.RoleDBTable, ChannelIdentities: &ir.ChannelIdentitiesDef{}}
	err := GoServerSQLiteErr(api, db, []string{"sqlite", "postgres"})
	if err == nil || !strings.Contains(err.Error(), "serves Alert: @event, Alert: @notifier, Identity: @channelIdentities on Postgres alone") {
		t.Fatalf("sqlite first: %v", err)
	}
	if err := GoServerSQLiteErr(api, db, []string{"postgres", "sqlite"}); err != nil {
		t.Fatalf("postgres first: %v", err)
	}
}

// TestGoServerSQLiteErrServesAccessAndRowFeatures: a Go server over a
// database that lists sqlite first is not refused an @audience table,
// grants, the access log, references or attachments, which the Go
// repositories serve on SQLite as on Postgres (#611); beside them, a
// feature the Go server serves on Postgres alone is still named, and they
// are not.
func TestGoServerSQLiteErrServesAccessAndRowFeatures(t *testing.T) {
	api := ir.NewSchema("app-api", ir.SchemaKindAPI)
	db := ir.NewSchema("app-db", ir.SchemaKindDB)
	db.Types["Folder"] = &ir.TypeDef{Name: "Folder", Role: ir.RoleDBTable, Audience: &ir.AudienceDef{}, RowReferences: &ir.RowReferencesDef{}, Attachments: &ir.AttachmentsDef{}}
	db.Types["Grant"] = &ir.TypeDef{Name: "Grant", Role: ir.RoleDBTable, AccessGrants: &ir.AccessGrantsDef{}}
	db.Types["AccessEntry"] = &ir.TypeDef{Name: "AccessEntry", Role: ir.RoleDBTable, AccessLog: &ir.AccessLogDef{}}
	if err := GoServerSQLiteErr(api, db, []string{"sqlite"}); err != nil {
		t.Fatalf("access and row features on sqlite: %v", err)
	}
	db.Types["Note"] = &ir.TypeDef{Name: "Note", Role: ir.RoleDBTable, Searchable: &ir.SearchableDef{}}
	err := GoServerSQLiteErr(api, db, []string{"sqlite"})
	if err == nil || !strings.Contains(err.Error(), "serves Note: @searchable on Postgres alone") || strings.Contains(err.Error(), "Folder") {
		t.Fatalf("search beside them on sqlite: %v", err)
	}
}

func TestGoServerSQLiteErrRefusesSearch(t *testing.T) {
	api := ir.NewSchema("app-api", ir.SchemaKindAPI)
	db := ir.NewSchema("app-db", ir.SchemaKindDB)
	db.Types["Note"] = &ir.TypeDef{Name: "Note", Role: ir.RoleDBTable, Searchable: &ir.SearchableDef{}}
	err := GoServerSQLiteErr(api, db, []string{"sqlite", "postgres"})
	if err == nil || !strings.Contains(err.Error(), "lists sqlite first, and serves Note: @searchable on Postgres alone so far (D152, D153, D155, D157, D158, D187, D190, D192)") {
		t.Fatalf("sqlite first: %v", err)
	}
	if err := GoServerSQLiteErr(api, db, []string{"postgres", "sqlite"}); err != nil {
		t.Fatalf("postgres first: %v", err)
	}
}
