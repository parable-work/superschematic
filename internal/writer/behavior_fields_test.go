package writer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestBehaviorFieldsRoundTrip (D103): a table whose behaviors sit on its
// fields is written in every format as its uses, `status: Workflow<{ ...
// }>` in TypeScript and `behaviors: [{ name, field, config }]` in the data
// forms, without the fields the loader adds for them or the @optimistic
// they imply, and reads back to the same IR, from a TypeScript schema and
// from a JSON one.
func TestBehaviorFieldsRoundTrip(t *testing.T) {
	for _, dir := range []string{tsFixtures + "/fixture-workflow-db", tsFixtures + "/fixture-workflow-fields-db"} {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			schema, err := loader.LoadService(dir)
			if err != nil {
				t.Fatalf("loading fixture: %v", err)
			}
			if len(schema.Types["Shipment"].OwnedFields()) == 0 || !schema.Types["Shipment"].Optimistic {
				t.Fatalf("Shipment = %+v, want the fields its uses add and @optimistic", schema.Types["Shipment"])
			}
			// A schema's description has no TypeScript form.
			schema.Description = ""
			want := normalizeIR(t, schema)
			for _, target := range []Format{FormatTS, FormatJSON, FormatYAML} {
				t.Run(string(target), func(t *testing.T) {
					if got := normalizeIR(t, writeAndReload(t, schema, target)); got != want {
						t.Errorf("IR mismatch after the %s round trip\nwant:\n%s\ngot:\n%s", target, want, got)
					}
				})
			}
		})
	}
}

// TestTSWriterWritesABehaviorAsItsFieldsType (D103): the TypeScript writer
// writes a use anchored on a field as that field's type, its config a type
// argument of literal types, imported from @superschematic/db, after the
// class's own fields, keeps an index that names an owned field, and leaves
// out the fields the loader adds and the @optimistic the uses imply.
func TestTSWriterWritesABehaviorAsItsFieldsType(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-workflow-db")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	dir := t.TempDir()
	written, err := WriteService(schema, FormatTS, dir)
	if err != nil {
		t.Fatal(err)
	}
	var source strings.Builder
	for _, rel := range written {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		source.Write(data)
	}
	text := source.String()
	for _, want := range []string{
		`import { AutoGenerate, Workflow, index, key, unique } from "@superschematic/db";`,
		"@index<Order>([\"status\", \"placedAt\"])\nexport abstract class Order {",
		"  title: string;\n\n  placedAt: Nullable<Temporal.DateTime>;\n\n" +
			`  status: Workflow<{ initial: "draft"; states: ["draft", "paid", "shipped"]; transitions: [{ from: "draft"; to: "paid" }, { from: "paid"; permission: "orders.ship"; to: "shipped" }] }>;` + "\n}\n",
		`  paymentStatus: Workflow<{ outcomes: { refunded: "failure" }; states: ["unpaid", "paid", "refunded"]; transitions: [{ from: "unpaid"; to: "paid" }, { from: "paid"; permission: "payments.refund"; to: "refunded" }] }>;`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("written TypeScript lacks %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"@optimistic", "EnteredAt", "@behavior"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("written TypeScript holds %q:\n%s", unwanted, text)
		}
	}
}
