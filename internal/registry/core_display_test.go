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
		"noun": "Ticket", "plural": "Tickets", "titleField": "title", "createLabel": "New ticket", "icon": "ticket",
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
		Noun: "Ticket", Plural: "Tickets", TitleField: "title", CreateLabel: "New ticket", Icon: "ticket",
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
		`{"icon": ""}`,
		`{"icon": " "}`,
		`{"icon": " ticket"}`,
		`{"icon": "ticket\n"}`,
		`{"icon": 1}`,
		`{"colour": "red"}`,
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

	// An icon is an icon name, by one explicit pattern in the spec and in
	// Go (ir.DisplayIconName), so every engine reads it alike: no \s or
	// "." that RE2, Ajv and Rust read apart.
	var args struct {
		Defs map[string]struct {
			Properties map[string]struct {
				Pattern string `json:"pattern"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(spec.Args, &args); err != nil {
		t.Fatal(err)
	}
	if got, want := args.Defs["TypeDisplay"].Properties["icon"].Pattern, ir.DisplayIconName.String(); got != want {
		t.Fatalf("the spec's icon pattern = %q, want ir.DisplayIconName's %q", got, want)
	}
	for icon, ok := range map[string]bool{
		"receipt": true, "mdi:receipt": true, "solid/box": true, "file-text": true, "ic_box": true, "2fa": true, "x.svg": true,
		"": false, " ": false, " ticket": false, "ticket ": false, "ticket\n": false, "a\rb": false, "a\u2028b": false,
		"\ufeffticket": false, "\u00a0ticket": false, "ticket\u00a0": false, "a b": false, "-ticket": false, ".ticket": false,
		"tick\u00e9t": false, "a\tb": false,
	} {
		err := spec.ValidateArgs([]any{map[string]any{"icon": icon}})
		if (err == nil) != ok {
			t.Errorf("ValidateArgs of the icon %q = %v, want ok %v", icon, err, ok)
		}
		if icon == "" {
			continue // an empty icon is absent in the IR
		}
		goErr := ir.ValidateTypeDisplay(&ir.TypeDisplay{Icon: icon})
		if (goErr == nil) != ok {
			t.Errorf("ir.ValidateTypeDisplay of the icon %q = %v, want ok %v", icon, goErr, ok)
		}
	}

	// An icon alone is a display.
	alone := &ir.TypeDef{Name: "Order"}
	if err := spec.ValidateArgs([]any{map[string]any{"icon": "receipt"}}); err != nil {
		t.Fatalf("ValidateArgs of an icon alone: %v", err)
	}
	if err := spec.Apply(Node{Type: alone}, []any{map[string]any{"icon": "receipt"}}, Site{}); err != nil || alone.Display == nil || alone.Display.Icon != "receipt" {
		t.Fatalf("Apply of an icon alone = %v, %+v", err, alone.Display)
	}
}

// @display({ multiline }) from @superschematic/schema on a field (D48,
// amended) is a spec of its own beside the type's: Args holds its argument
// to one boolean member, and Apply writes FieldDef.Display, once per field.
func TestAFieldDisplayDecorator(t *testing.T) {
	reg := New(naming.Naming{})
	spec, ok := reg.Decorator("display", TargetField)
	if !ok || !spec.DeclaredIn(pkgSchema) || spec.Args == nil {
		t.Fatalf("field @display spec = %+v", spec)
	}
	if _, ok := reg.Decorator("display", TargetOperation); ok {
		t.Fatal("an operation takes no @display")
	}
	for _, multiline := range []bool{true, false} {
		arg := map[string]any{"multiline": multiline}
		if err := spec.ValidateArgs([]any{arg}); err != nil {
			t.Fatalf("ValidateArgs(%v): %v", arg, err)
		}
		fd := &ir.FieldDef{Name: "notes"}
		if err := spec.Apply(Node{Field: fd}, []any{arg}, Site{}); err != nil {
			t.Fatalf("Apply(%v): %v", arg, err)
		}
		if fd.Display == nil || fd.Display.Multiline == nil || *fd.Display.Multiline != multiline {
			t.Fatalf("Display = %+v, want multiline %v", fd.Display, multiline)
		}
		err := spec.Apply(Node{Field: fd}, []any{arg}, Site{})
		if err == nil || err.Error() != "field notes has more than one @display decorator" {
			t.Fatalf("second @display: %v", err)
		}
	}

	for _, bad := range []string{
		`{}`,
		`{"multiline": "yes"}`,
		`{"multiline": 1}`,
		`{"multiline": null}`,
		`{"multiline": true, "rows": 6}`,
		`{"noun": "Note"}`,
		`true`,
	} {
		var value any
		if err := json.Unmarshal([]byte(bad), &value); err != nil {
			t.Fatal(err)
		}
		if err := spec.ValidateArgs([]any{value}); err == nil || !strings.HasPrefix(err.Error(), "@display argument:") {
			t.Errorf("ValidateArgs(%s) = %v", bad, err)
		}
	}

	// Apply, which IR built without the frontend's Args check still meets,
	// refuses a display that declares nothing and a non-boolean.
	fd := &ir.FieldDef{Name: "notes"}
	if err := spec.Apply(Node{Field: fd}, []any{map[string]any{}}, Site{}); err == nil || !strings.Contains(err.Error(), "invalid @display: display declares nothing") {
		t.Errorf("Apply({}) = %v", err)
	}
	if err := spec.Apply(Node{Field: fd}, []any{map[string]any{"multiline": "yes"}}, Site{}); err == nil || !strings.Contains(err.Error(), "@display: ") {
		t.Errorf("Apply of a non-boolean = %v", err)
	}
}
