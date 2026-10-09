package registry

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// @display from @superschematic/schema, on a class of any kind: Args holds
// its argument's shape and Apply writes TypeDef.Display, once per type.
func TestDisplayDecorator(t *testing.T) {
	reg := New(naming.Naming{})
	spec, ok := reg.Decorator("display", TargetType)
	if !ok || !spec.DeclaredIn(pkgSchema) || spec.Kinds != nil || spec.Args == nil {
		t.Fatalf("@display spec = %+v", spec)
	}
	var arg any
	if err := json.Unmarshal([]byte(`{
		"noun": "Ticket", "plural": "Tickets", "titleField": "title", "createLabel": "New ticket",
		"summaryFields": ["Workflow.status", "assignee"],
		"states": {"todo": {"label": "To do", "tone": "muted"}, "implementing": {"label": "Implement", "activeForm": "Implementing", "tone": "active"}},
		"transitions": {"todo": {"implementing": "Start"}}
	}`), &arg); err != nil {
		t.Fatal(err)
	}
	if err := spec.ValidateArgs([]any{arg}); err != nil {
		t.Fatalf("ValidateArgs: %v", err)
	}
	td := &ir.TypeDef{Name: "Ticket"}
	if err := spec.Apply(Node{Type: td}, []any{arg}, Site{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := &ir.TypeDisplay{
		Noun: "Ticket", Plural: "Tickets", TitleField: "title", CreateLabel: "New ticket",
		SummaryFields: []string{"Workflow.status", "assignee"},
		States: map[string]ir.DisplayState{
			"todo":         {Label: "To do", Tone: ir.DisplayToneMuted},
			"implementing": {Label: "Implement", ActiveForm: "Implementing", Tone: ir.DisplayToneActive},
		},
		Transitions: map[string]map[string]string{"todo": {"implementing": "Start"}},
	}
	if !reflect.DeepEqual(td.Display, want) {
		t.Fatalf("Display = %+v, want %+v", td.Display, want)
	}
	err := spec.Apply(Node{Type: td}, []any{map[string]any{"noun": "Again"}}, Site{})
	if err == nil || err.Error() != "type Ticket has more than one @display decorator" {
		t.Fatalf("second @display: %v", err)
	}

	for _, bad := range []string{
		`{}`,
		`{"noun": ""}`,
		`{"noun": "  "}`,
		`{"titleField": ""}`,
		`{"summaryFields": []}`,
		`{"summaryFields": ["a", "a"]}`,
		`{"states": {}}`,
		`{"states": {"to do": {"label": "To do"}}}`,
		`{"states": {"todo": {}}}`,
		`{"states": {"todo": {"tone": "blue"}}}`,
		`{"states": {"todo": {"label": "To do", "colour": "red"}}}`,
		`{"transitions": {"todo": {}}}`,
		`{"transitions": {"todo": {"in progress": "Start"}}}`,
		`{"transitions": {"todo": {"implementing": " "}}}`,
		`{"transitions": {"todo->implementing": "Start"}}`,
		`{"icon": "ticket"}`,
		`"Ticket"`,
	} {
		var value any
		if err := json.Unmarshal([]byte(bad), &value); err != nil {
			t.Fatal(err)
		}
		if err := spec.ValidateArgs([]any{value}); err == nil || !strings.HasPrefix(err.Error(), "@display argument:") {
			t.Errorf("ValidateArgs(%s) = %v", bad, err)
		}
	}
}
