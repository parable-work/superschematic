package loader

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// The fixture of D103's step 4's Derived and Provenance (D73): a database
// whose Ticket a model triages under review and whose Article a model
// summarizes.
const derivedProvenanceDBFixture = "tsreader/testdata/services/fixture-derived-provenance-db"

// TestDerivedFixtureLoads: the fixture loads with no warning and no error;
// Ticket and Article stay tables that implement Derived, and Ticket
// Provenance beside its status Workflow, each config read as the
// TypeScript form wrote it; the loader adds Provenance's owned inReview and
// each use's sub-table, Derived's derivations and Provenance's records,
// each keyed by the field and related to its row, and no queue, since
// neither arms a deadline, hears a write or emits an event; the layout
// gives each derived field its JSON Schema, an enum's values among it; and
// the expanded IR is held to its golden (D73, D103's step 4).
func TestDerivedFixtureLoads(t *testing.T) {
	var warnings []string
	warn := WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })
	db, err := LoadService(filepath.FromSlash(derivedProvenanceDBFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("the fixture's warnings =\n%q\nwant none", warnings)
	}
	ticket, article := db.Types["Ticket"], db.Types["Article"]
	if ticket == nil || ticket.IsEngineType() || !ticket.Optimistic || article == nil || !article.Optimistic {
		t.Fatalf("Ticket = %s, Article = %s", marshal(t, ticket), marshal(t, article))
	}
	var uses []string
	for _, ref := range ticket.Behaviors {
		uses = append(uses, ref.Name+"@"+ref.Field)
	}
	if want := []string{"Derived@", "Provenance@", "Workflow@status"}; !slices.Equal(uses, want) {
		t.Errorf("Ticket's uses = %v, want %v", uses, want)
	}
	if !strings.Contains(string(ticket.Behaviors[0].Config), `"states":["new","triage"]`) {
		t.Errorf("Ticket's Derived config = %s", ticket.Behaviors[0].Config)
	}
	owned := func(td *ir.TypeDef) []string {
		var out []string
		for _, fd := range td.Fields {
			if fd.Owned() {
				out = append(out, fd.Name+":"+fd.TypeRef.Name)
			}
		}
		return out
	}
	if got, want := owned(ticket), []string{"provenanceInReview:boolean", "status:string", "statusEnteredAt:Temporal.DateTime"}; !slices.Equal(got, want) {
		t.Errorf("Ticket's owned fields = %v, want %v", got, want)
	}
	if got := owned(article); len(got) != 0 {
		t.Errorf("Article's owned fields = %v, want none", got)
	}
	for _, sub := range []string{"TicketDerivedDerivations", "TicketProvenanceRecords", "ArticleDerivedDerivations"} {
		td := db.Types[sub]
		if td == nil || td.Origin != ir.OriginBehavior || td.BehaviorTable == nil || td.BehaviorTable.RowField == "" {
			t.Fatalf("%s = %s", sub, marshal(t, td))
		}
	}
	for _, name := range []string{"Ticket", "Article"} {
		for _, kind := range []string{ir.BehaviorQueueDeadlines, ir.BehaviorQueueChanges, ir.BehaviorQueueEvents} {
			if queue := db.BehaviorQueueOf(name, kind); queue != nil {
				t.Errorf("%s's %s queue = %s, want none", name, kind, queue.Name)
			}
		}
	}
	if got, want := string(behaviorlayout.DerivedOutput(db, ticket, "category")),
		`{"description":"A Category value","enum":["billing","bug","other",null],"type":["string","null"]}`; got != want {
		t.Errorf("category's output = %s, want %s", got, want)
	}
	if got := behaviorlayout.DerivedOutput(db, ticket, "subject"); got != nil {
		t.Errorf("subject's output = %s, want none: no Derived use derives it", got)
	}
	compareExpanded(t, db, "fixture-derived-provenance-db.expanded.json")
}

// TestDerivedAndProvenanceConfigsAreHeldToTheTable: the build refuses a
// table's Derived and Provenance config as the core would at its first
// command, in the core's words, from the data forms as from the
// TypeScript one: a derived field or an input the table lacks, a field
// derived from itself or another derived one, one Constants keeps, states
// with no Workflow or outside its states; and, which only the IR shows, a
// secret input (D74). Provenance: a recorded field the table lacks, a
// review source it does not compose, and gates with no Workflow, outside
// its states, or listing a field it does not record.
func TestDerivedAndProvenanceConfigsAreHeldToTheTable(t *testing.T) {
	entry := func(name string, from ...string) map[string]any {
		return map[string]any{name: map[string]any{"from": from, "instruction": "i", "model": "m"}}
	}
	derived := func(fields map[string]any) map[string]any {
		return map[string]any{"name": "Derived", "implements": true, "config": map[string]any{"fields": fields, "permission": "p"}}
	}
	stated := func(states ...string) map[string]any {
		return map[string]any{"summary": map[string]any{"from": []string{"body"}, "instruction": "i", "model": "m", "states": states}}
	}
	provenance := func(config map[string]any) map[string]any {
		return map[string]any{"name": "Provenance", "implements": true, "config": config}
	}
	workflow := map[string]any{"name": "Workflow", "field": "status", "config": map[string]any{
		"states": []any{"new", "closed"}, "transitions": []any{map[string]any{"from": "new", "to": "closed"}},
	}}
	both := entry("summary", "body")
	both["note"] = entry("note", "summary")["note"]
	for _, tc := range []struct {
		name string
		uses []any
		want string
	}{
		{"a derived field the table lacks", []any{derived(entry("title", "body"))}, "type Ticket: behavior Derived config: fields: Ticket has no field title"},
		{"a field derived from itself", []any{derived(entry("summary", "summary"))}, "type Ticket: behavior Derived config: fields: summary is derived from itself"},
		{"a field derived from another", []any{derived(both)}, "type Ticket: behavior Derived config: fields: note is derived from summary, which is derived too"},
		{"an input the table lacks", []any{derived(entry("summary", "notes"))}, "type Ticket: behavior Derived config: fields: summary is derived from notes, which Ticket does not have"},
		{"a secret input", []any{derived(entry("summary", "token"))}, "type Ticket: behavior Derived config: fields: summary is derived from token, which is secret: a secret never goes to a model"},
		{"a field Constants keeps", []any{map[string]any{"name": "Constants", "implements": true, "config": map[string]any{"fields": []any{"summary"}}}, derived(entry("summary", "body"))},
			"type Ticket: behavior Derived config: fields: summary is a field Constants keeps, which no model may write after the create"},
		{"states with no Workflow", []any{derived(stated("new"))}, "type Ticket: behavior Derived config: fields: summary names states, and the type has no Workflow"},
		{"a state the Workflow lacks", []any{derived(stated("done")), workflow}, `type Ticket: behavior Derived config: fields: summary: state "done" is not a state of the type's Workflow (new, closed)`},
		{"a recorded field the table lacks", []any{provenance(map[string]any{"fields": []any{"title"}})}, "type Ticket: behavior Provenance config: fields: Ticket has no field title"},
		{"a source the table does not compose", []any{provenance(map[string]any{"review": map[string]any{"sources": []any{"Derived"}, "permission": "p"}})},
			"type Ticket: behavior Provenance config: review: source Derived is not another behavior Ticket composes"},
		{"gates with no Workflow", []any{provenance(map[string]any{"gates": map[string]any{"closed": map[string]any{"reviewed": "all"}}})},
			"type Ticket: behavior Provenance config: gates: the type has no Workflow whose moves they gate"},
		{"a gate the Workflow lacks", []any{provenance(map[string]any{"gates": map[string]any{"done": map[string]any{"reviewed": "all"}}}), workflow},
			`type Ticket: behavior Provenance config: gate "done" is not a state of the type's Workflow (new, closed)`},
		{"a gate of a field not recorded", []any{provenance(map[string]any{"fields": []any{"body"}, "gates": map[string]any{"closed": map[string]any{"reviewed": []any{"summary"}}}}), workflow},
			`type Ticket: behavior Provenance config: gate "closed": summary is not a field Provenance records`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			field := func(name, typeName string, more map[string]any) map[string]any {
				f := map[string]any{"name": name, "typeRef": map[string]any{"name": typeName}}
				for k, v := range more {
					f[k] = v
				}
				return f
			}
			doc := map[string]any{
				"name": "tickets-db", "kind": "DB",
				"scalars": map[string]any{
					"Identity.UUID":     map[string]any{"name": "Identity.UUID", "languagePrimitive": "string"},
					"Temporal.DateTime": map[string]any{"name": "Temporal.DateTime", "languagePrimitive": "string"},
				},
				"types": map[string]any{
					"Ticket": map[string]any{"name": "Ticket", "role": "DBTable", "behaviors": tc.uses, "fields": []any{
						field("id", "Identity.UUID", map[string]any{"required": true, "key": true, "autoGenerated": true}),
						field("body", "string", map[string]any{"required": true}),
						field("summary", "string", nil),
						field("note", "string", nil),
						field("token", "string", map[string]any{"secret": true}),
					}},
				},
			}
			data, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			_, err = LoadService(writeService(t, map[string]string{
				"schema.config.json":      `{"name": "tickets-db", "kind": "DB", "outputs": {}}`,
				"src/tickets.schema.json": string(data),
			}))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want it to hold %q", err, tc.want)
			}
		})
	}
}
