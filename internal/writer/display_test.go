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
// transitions by key, and reading it back gives the same IR (the corpus
// covers the data-form legs). An embedded struct of a DB schema, which
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
@display({ noun: "Ticket", plural: "Tickets", titleField: "title", createLabel: "New ticket", summaryFields: ["Workflow.status", "assignee"], ` +
		`states: { done: { label: "Done", tone: "success" }, dropped: { label: "Dropped", tone: "danger" }, ` +
		`implementing: { label: "Implement", activeForm: "Implementing", tone: "active" }, review: { label: "Review", activeForm: "In review", tone: "warning" }, ` +
		`todo: { label: "To do", tone: "muted" } }, transitions: { implementing: { review: "Send to review" }, ` +
		`review: { done: "Accept", implementing: "Request changes" }, todo: { dropped: "Drop", implementing: "Start" } } })
export abstract class Ticket {`
	if !strings.Contains(string(got), want) {
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
