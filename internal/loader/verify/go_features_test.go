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
	db.Types["Ticket"].Searchable = &ir.SearchableDef{}
	if err := GoServerDatabaseErr(api, db); err == nil || !strings.Contains(err.Error(), "the Go server of app-api, over app-db, does not serve @searchable on Ticket yet (D155, D180)") {
		t.Fatalf("a searchable table: %v", err)
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
		{"@calendarSource", func(td *ir.TypeDef) { td.CalendarSources = []ir.CalendarSourceDef{{Field: "dueOn"}} }, "@calendarSource on Duty yet (D152, D180)"},
		{"Recurrence", func(td *ir.TypeDef) {
			td.Behaviors = []ir.BehaviorRef{{Name: "Recurrence", Implements: true}}
		}, "Recurrence on Duty yet (D152, D180)"},
		{"@event", func(td *ir.TypeDef) { td.Events = []*ir.EventDef{{Kind: "duty.due"}} }, "@event on Duty yet (D153, D180)"},
		{"@notifier", func(td *ir.TypeDef) { td.Notifier = &ir.NotifierDef{} }, "@notifier on Duty yet (D153, D180)"},
		{"@channelIdentities", func(td *ir.TypeDef) { td.ChannelIdentities = &ir.ChannelIdentitiesDef{} }, "@channelIdentities on Duty yet (D153, D180)"},
		{"@references", func(td *ir.TypeDef) { td.RowReferences = &ir.RowReferencesDef{} }, "@references on Duty yet (D154, D180)"},
		{"@attachments", func(td *ir.TypeDef) { td.Attachments = &ir.AttachmentsDef{} }, "@attachments on Duty yet (D154, D180)"},
		{"@searchable", func(td *ir.TypeDef) { td.Searchable = &ir.SearchableDef{} }, "@searchable on Duty yet (D155, D180)"},
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

	// Per-record access is served (D180).
	served := table("Document", func(td *ir.TypeDef) { td.Audience = &ir.AudienceDef{} })
	served.Types["Grant"] = &ir.TypeDef{Name: "Grant", Role: ir.RoleDBTable, AccessGrants: &ir.AccessGrantsDef{}}
	served.Types["AccessEntry"] = &ir.TypeDef{Name: "AccessEntry", Role: ir.RoleDBTable, AccessLog: &ir.AccessLogDef{}}
	if got := goFeatureFindings(ir.NewSchema("app-api", ir.SchemaKindAPI), Input{GoServer: true, Database: served}); len(got) != 0 {
		t.Fatalf("an audience, grants and the access log in Go: %v", got)
	}

	// An API's provider slots and an operation's autonomy.
	api := ir.NewSchema("app-api", ir.SchemaKindAPI)
	api.Types["AppConfig"] = &ir.TypeDef{Name: "AppConfig", Owner: "src/config.ts", ProviderSlots: map[string]string{"SMS": "channel"}}
	api.OperationSets = []*ir.OperationSet{{Name: "Duties", Operations: []*ir.FieldDef{{Name: "closeDuty", Autonomy: &ir.OperationAutonomy{}}}}}
	got := goFeatureFindings(api, Input{GoServer: true})
	if len(got) != 2 || !strings.Contains(got[0], "the provider slots of AppConfig (@providers) yet (D159, D180)") || !strings.Contains(got[1], "@autonomy on Duties.closeDuty yet (D158, D180)") {
		t.Fatalf("an API's slots and autonomy: %v", got)
	}
}
