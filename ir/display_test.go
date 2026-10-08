package ir

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func ticketDisplay() *TypeDisplay {
	return &TypeDisplay{
		Noun:          "Ticket",
		Plural:        "Tickets",
		TitleField:    "title",
		CreateLabel:   "New ticket",
		SummaryFields: []string{"Workflow.status", "assignee"},
		States: map[string]DisplayState{
			"todo":         {Label: "To do", Tone: DisplayToneMuted},
			"implementing": {Label: "Implement", ActiveForm: "Implementing", Tone: DisplayToneActive},
		},
		Transitions: map[string]map[string]string{"todo": {"implementing": "Start"}},
	}
}

// A type without a display marshals without the key, so the IR of every
// schema written before @display existed is the same bytes; a type with
// one writes it right after behaviors, its members in declaration order
// and its maps by key, and reads back the same through JSON and YAML.
func TestTypeDisplay_KeyPositionOmissionAndRoundTrip(t *testing.T) {
	plain := &TypeDef{Name: "T", Role: RoleEmbeddedStruct, Behaviors: []BehaviorRef{{Name: "Workflow"}}, RawHeritage: &RawHeritage{}}
	data, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"display"`)) {
		t.Fatalf("a type without a display wrote the key: %s", data)
	}

	plain.Display = ticketDisplay()
	data, err = json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	want := `"behaviors":[{"name":"Workflow"}],"display":{"noun":"Ticket","plural":"Tickets","titleField":"title","createLabel":"New ticket",` +
		`"summaryFields":["Workflow.status","assignee"],"states":{"implementing":{"label":"Implement","activeForm":"Implementing","tone":"active"},"todo":{"label":"To do","tone":"muted"}},` +
		`"transitions":{"todo":{"implementing":"Start"}}},"rawHeritage":`
	if !bytes.Contains(data, []byte(want)) {
		t.Fatalf("marshaled type = %s\nwant it to contain %s", data, want)
	}
	var back TypeDef
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&back, plain) {
		t.Fatalf("JSON round trip = %+v, want %+v", back.Display, plain.Display)
	}
	text, err := yaml.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	var fromYAML TypeDef
	if err := yaml.Unmarshal(text, &fromYAML); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&fromYAML, plain) {
		t.Fatalf("YAML round trip = %+v, want %+v\n%s", fromYAML.Display, plain.Display, text)
	}
}

func TestValidateTypeDisplay(t *testing.T) {
	if err := ValidateTypeDisplay(nil); err != nil {
		t.Fatalf("no display: %v", err)
	}
	if err := ValidateTypeDisplay(ticketDisplay()); err != nil {
		t.Fatalf("ticket display: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*TypeDisplay)
		want   string
	}{
		{"nothing", func(d *TypeDisplay) { *d = TypeDisplay{} }, "display declares nothing"},
		{"a blank noun", func(d *TypeDisplay) { d.Noun = " " }, "display noun must be non-blank"},
		{"a blank create label", func(d *TypeDisplay) { d.CreateLabel = "\t" }, "display createLabel must be non-blank"},
		{"no summary field", func(d *TypeDisplay) { d.SummaryFields = []string{} }, "display summaryFields must list a field"},
		{"a summary field twice", func(d *TypeDisplay) { d.SummaryFields = []string{"title", "title"} }, "display summaryFields lists title twice"},
		{"a state that is no name", func(d *TypeDisplay) { d.States["to do"] = DisplayState{Label: "To do"} }, `display states key "to do" is not a state name`},
		{"an empty state", func(d *TypeDisplay) { d.States["todo"] = DisplayState{} }, "display state todo declares nothing"},
		{"a blank state label", func(d *TypeDisplay) { d.States["todo"] = DisplayState{Label: " "} }, "display state todo label must be non-blank"},
		{"an unknown tone", func(d *TypeDisplay) { d.States["todo"] = DisplayState{Tone: "blue"} }, `display state todo tone "blue" is not one of muted, active, success, warning, danger`},
		{"a transition from no name", func(d *TypeDisplay) { d.Transitions["to do"] = map[string]string{"implementing": "Start"} }, `display transitions key "to do" is not a state name`},
		{"no transition from a state", func(d *TypeDisplay) { d.Transitions["todo"] = map[string]string{} }, "display transitions from todo label none"},
		{"a transition to no name", func(d *TypeDisplay) { d.Transitions["todo"] = map[string]string{"in progress": "Start"} }, `display transitions from todo key "in progress" is not a state name`},
		{"a blank transition label", func(d *TypeDisplay) { d.Transitions["todo"]["implementing"] = "" }, "display transition from todo to implementing must be non-blank"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := ticketDisplay()
			tc.change(d)
			err := ValidateTypeDisplay(d)
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}

	// Schema.Validate holds every type's display to the same shape.
	schema := NewSchema("tickets", SchemaKindGeneral)
	schema.Types["Ticket"] = &TypeDef{Name: "Ticket", Role: RoleEmbeddedStruct, Display: &TypeDisplay{Plural: " "}}
	errs := schema.Validate()
	if len(errs) != 1 || errs[0].Error() != "type Ticket: display plural must be non-blank" {
		t.Fatalf("Validate = %v", errs)
	}
}

// The tones are the closed set D48 records, in order.
func TestDisplayTones(t *testing.T) {
	got := make([]string, 0, len(DisplayTones()))
	for _, tone := range DisplayTones() {
		got = append(got, string(tone))
	}
	if strings.Join(got, ",") != "muted,active,success,warning,danger" {
		t.Fatalf("DisplayTones = %v", got)
	}
}
