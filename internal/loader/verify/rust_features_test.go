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
	got := rustFindings(db, Input{RustOutput: "outputs.types.rust"})
	want := []string{
		"type Agent: @agents (D158)",
		"type Identity: @channelIdentities (D153)",
		"type Notice: @notifier (D153)",
		"type RunRequest: @runRequests (D157)",
		"type Tenant: @party (D157)",
		"type Thread: @conversations (D157)",
		"type Ticket: @event (D153)",
	}
	if len(got) != len(want) {
		t.Fatalf("the Rust types' findings are %d, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i, prefix := range want {
		if !strings.Contains(got[i], prefix+" is served by the TypeScript runtime alone so far, and platform-db builds Rust (outputs.types.rust)") {
			t.Errorf("finding %d = %q, want %s", i, got[i], prefix)
		}
		if !strings.HasPrefix(got[i], "src/platform.schema.ts: ") {
			t.Errorf("finding %d = %q names no schema file", i, got[i])
		}
	}
	// The Rust types write Recurrence through the core, and serve an
	// audience, references and attachments (D185), a calendar source and
	// a search index (D188).
	served := []string{"Chore", "Folder", "Page", "Report", "Visit", "Article"}
	for _, finding := range got {
		for _, name := range served {
			if strings.Contains(finding, "type "+name+":") {
				t.Errorf("the Rust types refuse %q", finding)
			}
		}
	}
	if err := RefuseRustTypes(db); err == nil || !strings.Contains(err.Error(), "type Ticket: @event (D153)") {
		t.Errorf("RefuseRustTypes = %v, want the event refused", err)
	}

	// A Rust server: a provider of no Rust crate and autonomy, and its
	// database's table features. Its slots and a provider crate are served
	// (D185), and so are Recurrence, the calendar and search (D188).
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
	for _, want := range []string{
		"type provider relay-a: a provider of no Rust crate (providers/relay-a declares it in its package.json alone",
		"type Tickets.closeTicket: @autonomy (D158)",
		"type platform-db.Ticket: @event (D153)",
	} {
		if !strings.Contains(joined, want) || !strings.Contains(joined, "is served by the TypeScript runtime alone so far, and platform-api builds Rust (outputs.api.language: rust)") {
			t.Errorf("the Rust server's findings lack %s:\n%s", want, joined)
		}
	}
	for _, name := range []string{"listTickets", "Folder", "Config", "relay-b", "Chore", "Visit", "Article"} {
		if strings.Contains(joined, name+":") {
			t.Errorf("the Rust server refuses %s, which it serves:\n%s", name, joined)
		}
	}
	if err := RefuseRustServer(api, db); err == nil || !strings.Contains(err.Error(), "type platform-db.Ticket: @event (D153)") {
		t.Errorf("RefuseRustServer = %v, want the event refused", err)
	}
	if err := RefuseRustServer(ir.NewSchema("plain-api", ir.SchemaKindAPI)); err != nil {
		t.Errorf("RefuseRustServer of a plain API = %v", err)
	}
}
