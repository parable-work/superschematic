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
	if err := GoServerDatabaseErr(api, db); err == nil || !strings.Contains(err.Error(), "the Go server of app-api, over app-db, does not serve @party on Ticket yet (D157, D180)") {
		t.Fatalf("a party table: %v", err)
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
	for _, tc := range []struct {
		feature string
		edit    func(*ir.TypeDef)
		want    string
	}{
		{"@event", func(td *ir.TypeDef) { td.Events = []*ir.EventDef{{Kind: "duty.due"}} }, "@event on Duty yet (D153, D180)"},
		{"@notifier", func(td *ir.TypeDef) { td.Notifier = &ir.NotifierDef{} }, "@notifier on Duty yet (D153, D180)"},
		{"@channelIdentities", func(td *ir.TypeDef) { td.ChannelIdentities = &ir.ChannelIdentitiesDef{} }, "@channelIdentities on Duty yet (D153, D180)"},
		{"@party", func(td *ir.TypeDef) { td.Party = &ir.PartyDef{} }, "@party on Duty yet (D157, D180)"},
		{"@conversations", func(td *ir.TypeDef) { td.Conversations = &ir.ConversationsDef{} }, "@conversations on Duty yet (D157, D180)"},
		{"@runRequests", func(td *ir.TypeDef) { td.RunRequests = &ir.RunRequestsDef{} }, "@runRequests on Duty yet (D157, D180)"},
		{"@agents", func(td *ir.TypeDef) { td.Agents = &ir.AgentsDef{} }, "@agents on Duty yet (D158, D180)"},
	} {
		t.Run(tc.feature, func(t *testing.T) {
			schema := table("Duty", tc.edit)
			// The database's own Go ORM is its plain repositories.
			if got := goFeatureFindings(schema, Input{GoServer: true}); len(got) != 0 {
				t.Fatalf("the database: %v", got)
			}
			api := ir.NewSchema("app-api", ir.SchemaKindAPI)
			got := goFeatureFindings(api, Input{GoServer: true, Database: schema})
			if len(got) != 1 || !strings.Contains(got[0], "the Go server of app-api, over app-db, does not serve "+tc.want) {
				t.Fatalf("the Go server: %v, want %q", got, tc.want)
			}
			// An API served in TypeScript serves it.
			if got := goFeatureFindings(api, Input{Database: schema}); len(got) != 0 {
				t.Fatalf("a TypeScript server: %v", got)
			}
		})
	}

	// Per-record access is served (D180), references and attachments
	// (D184), and Recurrence, the calendar and search (D187).
	served := table("Document", func(td *ir.TypeDef) {
		td.Audience = &ir.AudienceDef{}
		td.RowReferences = &ir.RowReferencesDef{}
		td.Attachments = &ir.AttachmentsDef{Released: "FileReleased"}
		td.CalendarSources = []ir.CalendarSourceDef{{Field: "dueOn", Title: "title"}}
		td.Behaviors = []ir.BehaviorRef{{Name: "Recurrence", Implements: true}}
		td.Searchable = &ir.SearchableDef{}
		td.Fields = []*ir.FieldDef{{Name: "title", TypeRef: ir.TypeRef{Name: "string"}}}
	})
	served.Types["Grant"] = &ir.TypeDef{Name: "Grant", Role: ir.RoleDBTable, AccessGrants: &ir.AccessGrantsDef{}}
	served.Types["AccessEntry"] = &ir.TypeDef{Name: "AccessEntry", Role: ir.RoleDBTable, AccessLog: &ir.AccessLogDef{}}
	if got := goFeatureFindings(ir.NewSchema("app-api", ir.SchemaKindAPI), Input{GoServer: true, Database: served}); len(got) != 0 {
		t.Fatalf("an audience, grants, the access log, references, attachments, Recurrence, the calendar and search in Go: %v", got)
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
	if len(got) != 2 || !strings.Contains(got[0], "the Go server of app-api cannot link runtime provider relay-b (providers/relay-b): its directory holds no Go module") || !strings.Contains(got[1], "@autonomy on Duties.closeDuty yet (D158, D180)") {
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
	if err == nil || !strings.Contains(err.Error(), "lists sqlite first, and serves Filing: Recurrence, Filing: @calendarSource on Postgres alone so far (D152, D187)") {
		t.Fatalf("sqlite first: %v", err)
	}
	if err := GoServerSQLiteErr(api, db, []string{"postgres", "sqlite"}); err != nil {
		t.Fatalf("postgres first: %v", err)
	}
	delete(db.Types, "Filing")
	if err := GoServerSQLiteErr(api, db, []string{"sqlite"}); err != nil {
		t.Fatalf("a database with neither: %v", err)
	}
}
