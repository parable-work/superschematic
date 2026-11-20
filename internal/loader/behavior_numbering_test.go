package loader

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// The fixture of D103's step 4's Numbering: a database whose Invoice is
// numbered per customer and per year on its number field, and whose Ticket
// carries two Numbering fields that count apart.
const numberingDBFixture = "tsreader/testdata/services/fixture-numbering-db"

// TestNumberingFixtureLoads: the fixture loads with no warning and no
// error; Invoice and Ticket stay tables whose fields carry Numbering, each
// config read as the TypeScript form wrote it; the loader adds each use's
// label on the field's own column, optional, and its seq beside it, and for
// each use a shared table of its counts (InvoiceNumberCounts,
// invoice_number_counts), keyed by the scope alone with no relation to a
// row and @optimistic's version, and no queue, since Numbering arms no
// deadline, hears no write and emits no event; the index over the
// customer and the number takes the owned field; and the expanded IR is
// held to its golden (D103's step 4).
func TestNumberingFixtureLoads(t *testing.T) {
	var warnings []string
	warn := WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })
	db, err := LoadService(filepath.FromSlash(numberingDBFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("the fixture's warnings =\n%q\nwant none", warnings)
	}
	invoice, ticket := db.Types["Invoice"], db.Types["Ticket"]
	if invoice == nil || invoice.IsEngineType() || !invoice.Optimistic || ticket == nil || !ticket.Optimistic {
		t.Fatalf("Invoice = %s, Ticket = %s", marshal(t, invoice), marshal(t, ticket))
	}
	if refs := invoice.FieldBehaviors(); len(refs) != 1 || refs[0].Name != "Numbering" || refs[0].Field != "number" ||
		!strings.Contains(string(refs[0].Config), `"format":"INV-{yyyy}-{seq:4}"`) || !strings.Contains(string(refs[0].Config), `"scope":{"link":"customer"}`) {
		t.Errorf("Invoice's uses = %s", marshal(t, refs))
	}
	owned := func(td *ir.TypeDef) []string {
		var out []string
		for _, fd := range td.Fields {
			if fd.Owned() {
				out = append(out, fd.Name+":"+fd.TypeRef.Name+":"+map[bool]string{true: "required", false: "optional"}[fd.Required])
			}
		}
		return out
	}
	if want := []string{"numberSeq:Generic.Int64:optional", "number:string:optional"}; !slices.Equal(owned(invoice), want) {
		t.Errorf("Invoice's owned fields = %v, want %v", owned(invoice), want)
	}
	if want := []string{"numberSeq:Generic.Int64:optional", "number:string:optional", "referenceSeq:Generic.Int64:optional", "reference:string:optional"}; !slices.Equal(owned(ticket), want) {
		t.Errorf("Ticket's owned fields = %v, want %v", owned(ticket), want)
	}
	for name, use := range map[string]string{"InvoiceNumberCounts": "number", "TicketNumberCounts": "number", "TicketReferenceCounts": "reference"} {
		counts := db.Types[name]
		if counts == nil || counts.Origin != ir.OriginBehavior || counts.BehaviorTable == nil || counts.BehaviorTable.Use != use || counts.BehaviorTable.RowField != "" || !counts.Optimistic {
			t.Fatalf("%s = %s", name, marshal(t, counts))
		}
		var fields []string
		for _, fd := range counts.Fields {
			fields = append(fields, fd.Name)
			if fd.Relation != nil {
				t.Errorf("%s.%s relates to %s: a shared table holds no row's key", name, fd.Name, fd.Relation.Type)
			}
		}
		if !slices.Equal(fields, []string{"scope", "last", "counting"}) || !counts.Fields[0].Key {
			t.Errorf("%s's fields = %v, its first a key %v", name, fields, counts.Fields[0].Key)
		}
	}
	for _, kind := range []string{ir.BehaviorQueueDeadlines, ir.BehaviorQueueChanges, ir.BehaviorQueueEvents} {
		if queue := db.BehaviorQueueOf("Invoice", kind); queue != nil {
			t.Errorf("Invoice's %s queue = %s, want none", kind, queue.Name)
		}
	}
	if len(invoice.Indexes) != 1 || !slices.Equal(invoice.Indexes[0].Keys, []string{"customer", "number"}) || !invoice.Indexes[0].Unique {
		t.Errorf("Invoice's indexes = %s", marshal(t, invoice.Indexes))
	}
	compareExpanded(t, db, "fixture-numbering-db.expanded.json")
}

// numberingAPIFixture is the API that serves fixture-numbering-db's
// Invoice's and Ticket's behavior operations.
const numberingAPIFixture = "tsreader/testdata/services/fixture-numbering-api"

// TestNumberingAPIFixtureLoads: the API serves each Numbering field's
// assignNumber as a route of its row under the field, with no input member
// and its result typed by the declaration, an MCP tool each, with no
// worker, since Numbering keeps nothing for one to run, and no warning; and
// its expanded IR is held to its golden (D103's step 4).
func TestNumberingAPIFixtureLoads(t *testing.T) {
	var warnings []string
	warn := WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })
	api, err := LoadService(filepath.FromSlash(numberingAPIFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("the fixture's warnings =\n%q\nwant none", warnings)
	}
	var operations []string
	for _, set := range api.OperationSets {
		for _, op := range set.Operations {
			if marker := op.BehaviorOperation; marker != nil {
				var args []string
				for _, arg := range op.Arguments {
					args = append(args, arg.Name)
				}
				operations = append(operations, set.Name+"."+op.Name+" "+op.RestPath+" ("+strings.Join(args, ",")+") "+op.MCP.Handle+" "+marker.Use)
			}
		}
	}
	if want := []string{
		"InvoiceActions.numberAssignNumber invoice/{id}/number/assign-number (id) invoice_number_assign_number number",
		"TicketActions.numberAssignNumber ticket/{id}/number/assign-number (id) ticket_number_assign_number number",
		"TicketActions.referenceAssignNumber ticket/{id}/reference/assign-number (id) ticket_reference_assign_number reference",
	}; !slices.Equal(operations, want) {
		t.Errorf("the behavior operations =\n%s\nwant\n%s", strings.Join(operations, "\n"), strings.Join(want, "\n"))
	}
	if len(api.Workers) != 0 {
		t.Errorf("the API's workers = %s, want none", marshal(t, api.Workers))
	}
	compareExpanded(t, api, "fixture-numbering-api.expanded.json")
}

// TestNumberingConfigsAreHeldToTheTable: the build holds a table's
// Numbering config to the table in the core's words, from the JSON data
// form as from the TypeScript one: a token it does not know, a format with
// no {seq}, a yearly reset that does not print the year, a scope that
// names no to-one relation of the table, and one whose relation is not
// required.
func TestNumberingConfigsAreHeldToTheTable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		config   map[string]any
		required bool
		want     string
	}{
		{"a token it does not know", map[string]any{"format": "INV-{n}"}, true, `type Invoice: field number: behavior Numbering config: format "INV-{n}" has {n}, which is not one of its tokens: {seq}, {seq:N} (N from 1 to 10), {yyyy} and {yy}`},
		{"no seq", map[string]any{"format": "INV-{yyyy}"}, true, `type Invoice: field number: behavior Numbering config: format "INV-{yyyy}" has no {seq}: it prints the number with {seq}, or {seq:N} padded to N digits, once`},
		{"a yearly reset without the year", map[string]any{"format": "INV-{seq}", "reset": "yearly"}, true, `type Invoice: field number: behavior Numbering config: reset yearly counts each year from 1, so format "INV-{seq}" prints the year, {yyyy} or {yy}, or two years would print one label`},
		{"a scope of no relation", map[string]any{"format": "INV-{seq}", "scope": map[string]any{"link": "title"}}, true, `type Invoice: field number: behavior Numbering config: scope.link title is not a to-one relation of Invoice (its to-one relations: customer), so a row has no one target to count by`},
		{"a scope that is not required", map[string]any{"format": "INV-{seq}", "scope": map[string]any{"link": "customer"}}, false, `type Invoice: field number: behavior Numbering config: scope.link customer is not required, so a row may hold no target to count by; make it required`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			field := func(name, typeName string, more map[string]any) map[string]any {
				f := map[string]any{"name": name, "typeRef": map[string]any{"name": typeName}}
				for k, v := range more {
					f[k] = v
				}
				return f
			}
			key := map[string]any{"required": true, "key": true, "autoGenerated": true}
			doc := map[string]any{
				"name": "invoices-db", "kind": "DB",
				"scalars": map[string]any{
					"Identity.UUID":     map[string]any{"name": "Identity.UUID", "languagePrimitive": "string"},
					"Temporal.DateTime": map[string]any{"name": "Temporal.DateTime", "languagePrimitive": "string"},
				},
				"types": map[string]any{
					"Customer": map[string]any{"name": "Customer", "role": "DBTable", "fields": []any{
						field("id", "Identity.UUID", key),
						field("name", "string", map[string]any{"required": true}),
					}},
					"Invoice": map[string]any{"name": "Invoice", "role": "DBTable",
						"behaviors": []any{map[string]any{"name": "Numbering", "field": "number", "config": tc.config}},
						"fields": []any{
							field("id", "Identity.UUID", key),
							field("customer", "Customer", map[string]any{"required": tc.required, "relation": map[string]any{"type": "Customer"}}),
							field("title", "string", map[string]any{"required": true}),
							field("createdAt", "Temporal.DateTime", map[string]any{"required": true}),
						}},
				},
			}
			data, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			_, err = LoadService(writeService(t, map[string]string{
				"schema.config.json":       `{"name": "invoices-db", "kind": "DB", "outputs": {}}`,
				"src/invoices.schema.json": string(data),
			}))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want it to hold %q", err, tc.want)
			}
		})
	}
}
