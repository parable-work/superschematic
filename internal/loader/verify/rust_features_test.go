package verify

import (
	"encoding/json"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// rustFeaturesDB is a DB schema with a table of each feature of D152 to
// D159 the Rust outputs refuse (D181), the tables of those they serve
// (an @audience, references, attachments, D185; Recurrence, a calendar
// source and @searchable, D188), and a table of none.
func rustFeaturesDB() *ir.Schema {
	schema := ir.NewSchema("platform-db", ir.SchemaKindDB)
	table := func(name string, edit func(*ir.TypeDef)) {
		td := &ir.TypeDef{Name: name, Role: ir.RoleDBTable, Owner: "src/platform.schema.ts"}
		edit(td)
		schema.Types[name] = td
	}
	table("Visit", func(td *ir.TypeDef) { td.CalendarSources = []ir.CalendarSourceDef{{Field: "at"}} })
	table("Chore", func(td *ir.TypeDef) {
		td.Behaviors = []ir.BehaviorRef{{Name: recurrenceBehavior, Implements: true, Config: json.RawMessage(`{}`)}}
	})
	table("Ticket", func(td *ir.TypeDef) { td.Events = []*ir.EventDef{{Kind: "ticket.opened"}} })
	table("Notice", func(td *ir.TypeDef) { td.Notifier = &ir.NotifierDef{} })
	table("Identity", func(td *ir.TypeDef) { td.ChannelIdentities = &ir.ChannelIdentitiesDef{} })
	table("Page", func(td *ir.TypeDef) { td.RowReferences = &ir.RowReferencesDef{} })
	table("Report", func(td *ir.TypeDef) { td.Attachments = &ir.AttachmentsDef{} })
	table("Article", func(td *ir.TypeDef) { td.Searchable = &ir.SearchableDef{} })
	table("Tenant", func(td *ir.TypeDef) { td.Party = &ir.PartyDef{} })
	table("Thread", func(td *ir.TypeDef) { td.Conversations = &ir.ConversationsDef{} })
	table("RunRequest", func(td *ir.TypeDef) { td.RunRequests = &ir.RunRequestsDef{} })
	table("Agent", func(td *ir.TypeDef) { td.Agents = &ir.AgentsDef{} })
	table("Folder", func(td *ir.TypeDef) { td.Audience = &ir.AudienceDef{Owner: "owner"} })
	return schema
}

// rustFindings are checkRustFeatures' errors of schema for in.
func rustFindings(schema *ir.Schema, in Input) []string {
	r := &Result{}
	checkRustFeatures(schema, in, r)
	var out []string
	for _, err := range r.Errors {
		out = append(out, err.Error())
	}
	return out
}

func TestRustOutputsRefuseThePlatformFeaturesTheyDoNotServe(t *testing.T) {
	db := rustFeaturesDB()
	// A service that builds no Rust is refused nothing.
	if got := rustFindings(db, Input{}); len(got) != 0 {
		t.Fatalf("a service with no Rust output has findings: %v", got)
	}
	// On Postgres the Rust types serve every table feature of D152 to
	// D159: Recurrence through the core, an audience, references and
	// attachments (D185), a calendar source and a search index (D188),
	// events, the notifier's tables and the channel identities (D191), and
	// parties, the conversations, the run requests and the agent
	// orchestration (D193).
	if got := rustFindings(db, Input{RustOutput: "outputs.types.rust", Dialects: []string{"postgres"}}); len(got) != 0 {
		t.Fatalf("the Rust types on Postgres have findings:\n%s", strings.Join(got, "\n"))
	}
	// On SQLite too since #618: D157's and D158's tables, whose queues
	// the Rust runtime claims on the file.
	if got := rustFindings(db, Input{RustOutput: "outputs.types.rust", Dialects: []string{"postgres", "sqlite"}}); len(got) != 0 {
		t.Fatalf("the Rust types on SQLite have findings:\n%s", strings.Join(got, "\n"))
	}
	for _, sqlite := range []bool{false, true} {
		if err := RefuseRustTypes(db, sqlite); err != nil {
			t.Errorf("RefuseRustTypes (sqlite %v) = %v, want nothing refused", sqlite, err)
		}
	}

	// A Rust server: a provider of no Rust crate. Its slots and a provider
	// crate are served (D185), Recurrence, the calendar and search (D188),
	// and an operation's @autonomy and its database's parties and agents
	// (D193); rustrestgen refuses those on a database that lists sqlite
	// first.
	api := ir.NewSchema("platform-api", ir.SchemaKindAPI)
	api.Types["Config"] = &ir.TypeDef{Name: "Config", EnvVars: true, ProviderSlots: map[string]string{"SMS": "channel"}}
	api.Providers = []*ir.RuntimeProvider{
		{Name: "relay-a", Implements: "channel", Package: "@example/relay-a", Path: "providers/relay-a"},
		{Name: "relay-b", Implements: "channel", Crate: "relay-b", Path: "providers/relay-b"},
	}
	api.Types["Tickets"] = &ir.TypeDef{Name: "Tickets", Fields: []*ir.FieldDef{
		{Name: "closeTicket", Autonomy: &ir.OperationAutonomy{}},
		{Name: "listTickets"},
	}}
	server := rustFindings(api, Input{RustOutput: "outputs.api.language: rust", Database: db})
	joined := strings.Join(server, "\n")
	if len(server) != 1 || !strings.Contains(joined, "type provider relay-a: a provider of no Rust crate (providers/relay-a declares it in its package.json alone") || !strings.Contains(joined, "is served by the TypeScript runtime alone so far, and platform-api builds Rust (outputs.api.language: rust)") {
		t.Errorf("the Rust server's findings are not the provider of no crate alone:\n%s", joined)
	}
	if err := RefuseRustServer(api, db); err == nil || !strings.Contains(err.Error(), "type provider relay-a") || strings.Contains(err.Error(), "closeTicket") || strings.Contains(err.Error(), "Tenant") {
		t.Errorf("RefuseRustServer = %v, want the provider of no crate alone refused", err)
	}
	if err := RefuseRustServer(ir.NewSchema("plain-api", ir.SchemaKindAPI)); err != nil {
		t.Errorf("RefuseRustServer of a plain API = %v", err)
	}
}
