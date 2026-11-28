package behaviortypes

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// Recurrence (#601): the config's anchors are the alias <Base>Anchor, in
// the declaration's order, which setRecurrence's anchor names; a member
// only anchors the config leaves out take is gone from setRecurrence; the
// result keeps the declaration's shape; nextDueAt and lastDoneAt are epoch
// milliseconds, absent on a row with none.
func TestNarrowRecurrence(t *testing.T) {
	reg := registry.New(naming.Default())
	declaration, ok := reg.Behavior("Recurrence")
	if !ok {
		t.Fatal("no Recurrence")
	}
	table := &ir.TypeDef{Name: "Filing", Behaviors: []ir.BehaviorRef{{Name: "Recurrence", Implements: true}}}
	target := Target{Schema: "filings", Base: "FilingRecurrence", Document: ir.NewSchema("filings", ir.SchemaKindDB), Instance: table}
	for _, test := range []struct {
		config  string
		anchors []string
		members []string
	}{
		{`{"anchors": ["relative", "calendar", "afterDone"], "relative": {"via": "contract", "type": "Contract", "field": "endsOn"}}`, []string{"calendar", "afterDone", "relative"}, []string{"adjust", "anchor", "every", "offset", "rule", "start", "timeZone"}},
		{`{"anchors": ["calendar"]}`, []string{"calendar"}, []string{"adjust", "anchor", "rule", "start", "timeZone"}},
		{`{"anchors": ["afterDone"]}`, []string{"afterDone"}, []string{"adjust", "anchor", "every", "start", "timeZone"}},
		{`{"anchors": ["relative"], "relative": {"via": "contract", "type": "Contract", "field": "endsOn"}}`, []string{"relative"}, []string{"adjust", "anchor", "every", "offset", "timeZone"}},
	} {
		n, err := Narrow(declaration, json.RawMessage(test.config), target)
		if err != nil {
			t.Fatalf("%s: %v", test.config, err)
		}
		if len(n.Aliases) != 1 || n.Aliases[0].Name != "FilingRecurrenceAnchor" {
			t.Fatalf("%s: aliases %v", test.config, n.Aliases)
		}
		if got := stringsOf(n.Aliases[0].Schema.(map[string]any)["enum"]); !slices.Equal(got, test.anchors) {
			t.Errorf("%s: anchors %v, want %v", test.config, got, test.anchors)
		}
		if want := "An anchor a FilingRecurrence's Recurrence takes for a row's recurrence."; n.Aliases[0].Doc != want {
			t.Errorf("doc %q, want %q", n.Aliases[0].Doc, want)
		}
		properties, err := at(n.Params["setRecurrence"], "properties")
		if err != nil {
			t.Fatal(err)
		}
		if got := SortedKeys(properties); !slices.Equal(got, test.members) {
			t.Errorf("%s: setRecurrence takes %v, want %v", test.config, got, test.members)
		}
		anchor, _ := properties["anchor"].(map[string]any)
		if anchor[RefKey] != "FilingRecurrenceAnchor" || anchor["description"] == nil {
			t.Errorf("%s: anchor %v", test.config, anchor)
		}
		// A create's recurrence takes what setRecurrence takes (#729).
		created, err := at(n.CreateParams, "properties", "recurrence", "properties")
		if err != nil {
			t.Fatal(err)
		}
		if got := SortedKeys(created); !slices.Equal(got, test.members) {
			t.Errorf("%s: a create's recurrence takes %v, want %v", test.config, got, test.members)
		}
		if anchor, _ := created["anchor"].(map[string]any); anchor[RefKey] != "FilingRecurrenceAnchor" {
			t.Errorf("%s: a create's anchor %v", test.config, anchor)
		}
		// The result's recurrence is the declaration's object.
		if node, err := at(n.Results["setRecurrence"], "properties", "recurrence"); err != nil || node[RefKey] != nil {
			t.Errorf("%s: the result's recurrence %v (%v)", test.config, node, err)
		}
		for _, field := range []string{"nextDueAt", "lastDoneAt"} {
			shape := n.Fields[field]
			if schema, _ := shape.Schema.(map[string]any); schema["type"] != "integer" || shape.Present {
				t.Errorf("%s: %s = %+v, want an integer absent before", test.config, field, shape)
			}
		}
		if _, ok := n.Fields["recurrence"]; !ok {
			t.Errorf("%s: no recurrence field", test.config)
		}
	}
}
