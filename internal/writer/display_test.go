package writer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemafile"
	ir "github.com/parable-work/superschematic/ir"
)

// TestTSWriterEmitsDisplay: the TypeScript writer emits @display after the
// type's behaviors, its members in the IR's order and its states and
// transitions by key, and a field's @display beside its title and icon,
// and reading it back gives the same IR (the corpus covers the data-form
// legs). An embedded struct of a DB schema, which
// renders as a type alias, cannot carry it, and the writer says so.
func TestTSWriterEmitsDisplay(t *testing.T) {
	schema, err := loader.LoadService(dataFixtures + "/fixture-display-json")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := WriteService(schema, FormatTS, dir); err != nil {
		t.Fatalf("WriteService: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "src", "ticket.schema.ts"))
	if err != nil {
		t.Fatal(err)
	}
	want := `@behavior("Comments")
@display({ noun: "Ticket", plural: "Tickets", titleField: "title", createLabel: "New ticket", icon: "ticket", summaryFields: ["Workflow.status", "assignee"], ` +
		`states: { done: { label: "Done", tone: "success" }, dropped: { label: "Dropped", tone: "danger" }, ` +
		`implementing: { label: "Implement", activeForm: "Implementing", tone: "active" }, review: { label: "Review", activeForm: "In review", tone: "warning" }, ` +
		`todo: { label: "To do", tone: "muted" } }, transitions: { implementing: { review: "Send to review" }, ` +
		`review: { done: "Accept", implementing: "Request changes" }, todo: { dropped: "Drop", implementing: "Start" } } })
export abstract class Ticket {`
	if !strings.Contains(string(got), want) {
		t.Fatalf("TS output missing\n%s\n\ngot:\n%s", want, got)
	}
	// A field's display is a decorator of its own beside the field's
	// title and icon (D48, amended).
	if want := "  @display({ multiline: true })\n  body: Nullable<string>;\n"; !strings.Contains(string(got), want) {
		t.Fatalf("TS output missing\n%s\n\ngot:\n%s", want, got)
	}
	if got, want := normalizeIR(t, writeAndReload(t, schema, FormatTS)), normalizeIR(t, schema); got != want {
		t.Fatalf("IR changed after a JSON -> TS round trip\nwant:\n%s\ngot:\n%s", want, got)
	}

	alias := &schemafile.Document{Name: "fixture", Kind: ir.SchemaKindDB, Types: map[string]*ir.TypeDef{
		"Address": {Name: "Address", Role: ir.RoleEmbeddedStruct, Display: &ir.TypeDisplay{Noun: "Address"}},
	}}
	if _, err := Write(alias, FormatTS); err == nil || !strings.Contains(err.Error(), "type Address: embedded structs in a DB schema render as type aliases and cannot carry heritage or decorators") {
		t.Fatalf("Write(ts) of an aliased struct with a display = %v", err)
	}
}

// TestEnumTitlesRoundTrip: an enum member's title (D48, amended) goes from
// TypeScript to IR to TypeScript, JSON and YAML and back to equal IR. The
// TypeScript writer puts the @title tag on a line of its own after the
// member's comment, so a reread keeps the comment's lines in order; and it
// refuses a comment that holds the tag, which the reader would refuse.
func TestEnumTitlesRoundTrip(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-display")
	if err != nil {
		t.Fatal(err)
	}
	priority := schema.Enums["Priority"]
	if priority == nil || len(priority.Values) != 3 {
		t.Fatalf("Priority = %+v", priority)
	}
	// Title every member, one with a comment of two lines.
	priority.Values[0].Title = "Whenever"
	priority.Values[0].Comment = "No rush.\nSome @titles text that is no tag."
	dir := t.TempDir()
	if _, err := WriteService(schema, FormatTS, dir); err != nil {
		t.Fatalf("WriteService: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "src", "ticket.schema.ts"))
	if err != nil {
		t.Fatal(err)
	}
	want := `export enum Priority {
  // No rush.
  // Some @titles text that is no tag.
  /** @title Whenever */
  Low = "low",
  // Within the week.
  /** @title Needs attention */
  High = "high",
  // Drop everything else.
  /** @title Drop everything */
  Urgent = "urgent"
}`
	if !strings.Contains(string(got), want) {
		t.Fatalf("TS output missing\n%s\n\ngot:\n%s", want, got)
	}
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		if got, want := normalizeIR(t, writeAndReload(t, schema, format)), normalizeIR(t, schema); got != want {
			t.Fatalf("IR changed after a TS -> %s round trip\nwant:\n%s\ngot:\n%s", format, want, got)
		}
	}

	for _, comment := range []string{"Not urgent.\n@title Whenever", "Says @title in passing.", "Not urgent (@title:Whenever)."} {
		tagged := &schemafile.Document{Name: "fixture", Kind: ir.SchemaKindGeneral, Enums: map[string]*ir.EnumDef{
			"Priority": {Name: "Priority", Values: []ir.EnumValueDef{{Name: "Low", Comment: comment}}},
		}}
		if _, err := Write(tagged, FormatTS); err == nil || !strings.Contains(err.Error(), "enum Priority.Low: a comment that holds @title has no TypeScript authoring form") {
			t.Fatalf("Write(ts) of the comment %q, which holds the tag = %v", comment, err)
		}
	}
	for _, comment := range []string{"Not urgent.\n@titles are fine", "Mail a@title.example.", "@title-case is no tag."} {
		tagged := &schemafile.Document{Name: "fixture", Kind: ir.SchemaKindGeneral, Enums: map[string]*ir.EnumDef{
			"Priority": {Name: "Priority", Values: []ir.EnumValueDef{{Name: "Low", Comment: comment}}},
		}}
		if _, err := Write(tagged, FormatTS); err != nil {
			t.Fatalf("Write(ts) of the comment %q, which holds no tag = %v", comment, err)
		}
	}
}

// TestFieldDisplayCurrencyRoundTrips: a field's @display currency (D48,
// amended), a code or a field, beside multiline in one argument, goes
// from YAML to IR to TypeScript, JSON and YAML and back to equal IR; the
// TypeScript writer writes the code as a string and the field as
// { field }.
func TestFieldDisplayCurrencyRoundTrips(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"schema.config.yaml": "name: prices\nkind: General\noutputs: {}\n",
		"src/price.schema.yaml": `name: prices
kind: General
scalars:
  Finance.Money: { name: Finance.Money, languagePrimitive: number }
  Finance.Currency: { name: Finance.Currency, languagePrimitive: string }
  Generic.Int64: { name: Generic.Int64, languagePrimitive: number }
types:
  Price:
    name: Price
    role: EmbeddedStruct
    fields:
      - name: amount
        typeRef: { name: Finance.Money }
        required: true
        display: { currency: GBP }
      - name: tiers
        typeRef: { name: Generic.Int64, isArray: true }
        required: true
        display: { currency: { field: currency } }
      - name: currency
        typeRef: { name: Finance.Currency }
        required: true
      - name: note
        typeRef: { name: string }
        required: true
        display: { multiline: true }
`,
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := loader.LoadService(dir)
	if err != nil {
		t.Fatal(err)
	}
	ts := writtenSource(t, schema, FormatTS)
	for _, want := range []string{
		"  @display({ currency: \"GBP\" })\n  amount: Finance.Money;\n",
		"  @display({ currency: { field: \"currency\" } })\n  tiers: Generic.Int64[];\n",
		"  @display({ multiline: true })\n  note: string;\n",
	} {
		if !strings.Contains(ts, want) {
			t.Errorf("TS output missing\n%s\ngot:\n%s", want, ts)
		}
	}
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		if got, want := normalizeIR(t, writeAndReload(t, schema, format)), normalizeIR(t, schema); got != want {
			t.Errorf("IR changed after a YAML -> %s round trip\nwant:\n%s\ngot:\n%s", format, want, got)
		}
	}
	both := cloneSchema(t, schema)
	yes := true
	both.Types["Price"].Fields[0].Display.Multiline = &yes
	if got := writtenSource(t, both, FormatTS); !strings.Contains(got, "@display({ multiline: true, currency: \"GBP\" })") {
		t.Errorf("TS output of two members:\n%s", got)
	}
}
