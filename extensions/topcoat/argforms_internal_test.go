package topcoat

import (
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// TestArgumentControls gives each argument the control its type and place
// call for: an input form's control for its type, a group for a list of an
// enum, an input per value for any other list, a hidden input for a path
// argument, a select of true and false for a filter's boolean, and JSON
// text for a value no control holds.
func TestArgumentControls(t *testing.T) {
	schemas := schemaSet{{
		Enums: map[string]*ir.EnumDef{"Shade": {Name: "Shade", Values: []ir.EnumValueDef{{Name: "Light", SerializedAs: "light"}}}},
		Types: map[string]*ir.TypeDef{"Point": {Name: "Point"}},
		Scalars: map[string]*ir.ScalarDef{
			"Generic.JSON":      {Name: "Generic.JSON", TypeMappings: map[string]string{"json_schema": "object"}},
			"Temporal.DateTime": {Name: "Temporal.DateTime", LanguagePrimitive: ir.LanguageString},
		},
	}}
	maxLength := 4
	for _, tc := range []struct {
		name     string
		ref      ir.TypeRef
		rustType string
		location string
		filter   bool
		kind     string
		control  string
		read     string
		attrs    string
	}{
		{name: "id", ref: ir.TypeRef{Name: "string"}, rustType: "String", location: "path", kind: kindPath, control: "hidden", read: "text"},
		{name: "note", ref: ir.TypeRef{Name: "string"}, rustType: "Option<String>", location: "body", kind: kindValue, control: "text", read: "text", attrs: ` maxlength="4"`},
		{name: "shade", ref: ir.TypeRef{Name: "Shade"}, rustType: "types::Shade", location: "body", kind: kindValue, control: "select", read: "text"},
		{name: "shades", ref: ir.TypeRef{Name: "Shade", IsArray: true}, rustType: "Option<Vec<types::Shade>>", location: "query", filter: true, kind: kindGroup, control: "select", read: "text"},
		{name: "codes", ref: ir.TypeRef{Name: "string", IsArray: true}, rustType: "Vec<String>", location: "query", filter: true, kind: kindRepeated, control: "text", read: "text", attrs: ` maxlength="4"`},
		{name: "limit", ref: ir.TypeRef{Name: "number"}, rustType: "Option<f64>", location: "query", filter: true, kind: kindValue, control: "number", read: "number", attrs: ` step="any"`},
		{name: "all", ref: ir.TypeRef{Name: "boolean"}, rustType: "Option<bool>", location: "query", filter: true, kind: kindValue, control: "select", read: "yes_no"},
		{name: "notify", ref: ir.TypeRef{Name: "boolean"}, rustType: "Option<bool>", location: "body", kind: kindValue, control: "checkbox", read: "boolean"},
		{name: "on", ref: ir.TypeRef{Name: "boolean"}, rustType: "bool", location: "path", kind: kindPath, control: "hidden", read: "yes_no"},
		{name: "at", ref: ir.TypeRef{Name: "Temporal.DateTime"}, rustType: "Option<DateTime>", location: "body", kind: kindValue, control: "datetime-local", read: "date_time"},
		{name: "labels", ref: ir.TypeRef{Name: "string", IsMap: true}, rustType: "HashMap<String, String>", location: "body", kind: kindValue, control: "textarea", read: "json_text"},
		{name: "grid", ref: ir.TypeRef{Name: "number", IsArray: true, IsArrayOfArrays: true}, rustType: "Vec<Vec<f64>>", location: "body", kind: kindValue, control: "textarea", read: "json_text"},
		{name: "point", ref: ir.TypeRef{Name: "Point"}, rustType: "types::Point", location: "body", kind: kindValue, control: "textarea", read: "json_text"},
		{name: "flags", ref: ir.TypeRef{Name: "boolean", IsArray: true}, rustType: "Vec<bool>", location: "body", kind: kindValue, control: "textarea", read: "json_text"},
		{name: "extra", ref: ir.TypeRef{Name: "Generic.JSON"}, rustType: "serde_json::Value", location: "body", kind: kindValue, control: "textarea", read: "json_text"},
	} {
		arg := &ir.ArgumentDef{Name: tc.name, TypeRef: tc.ref}
		if tc.name == "note" || tc.name == "codes" {
			arg.ValidateMaxLength = &maxLength
		}
		param := registry.RustParam{Name: tc.name, Field: tc.name, RustType: tc.rustType}
		f := schemas.argField(arg, param, tc.location, tc.filter)
		if f.Kind != tc.kind || f.Control != tc.control || f.Read != tc.read || f.Attrs != tc.attrs {
			t.Errorf("%s: kind %q control %q read %q attrs %q; want %q %q %q %q", tc.name, f.Kind, f.Control, f.Read, f.Attrs, tc.kind, tc.control, tc.read, tc.attrs)
		}
	}
	at := schemas.argField(&ir.ArgumentDef{Name: "holdUntil", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}}, registry.RustParam{Name: "holdUntil", Field: "hold_until", RustType: "Option<DateTime>"}, "body", false)
	if at.Label != "Hold until (UTC)" {
		t.Errorf("a date-time argument's label says it is read as UTC: %q", at.Label)
	}
}

// TestBooleanArguments reads a boolean argument with a declared default as
// a browser sends it. An action's checkbox sends nothing when it is not
// checked, which is false whatever the default, so the default only checks
// a new form's box when it is true. A filter's select sends nothing for its
// blank option, which reads the default.
func TestBooleanArguments(t *testing.T) {
	schemas := schemaSet{{}}
	boolean := func(name, def string, filter bool) *argField {
		arg := &ir.ArgumentDef{Name: name, TypeRef: ir.TypeRef{Name: "boolean"}, Default: &def}
		location := "body"
		if filter {
			location = "query"
		}
		return schemas.argField(arg, registry.RustParam{Name: name, Field: name, RustType: "Option<bool>"}, location, filter)
	}
	for _, tc := range []struct {
		field         *argField
		read, newForm string
	}{
		{boolean("notify", "true", false), `single(&mut errors, "notify", boolean(self.notify.as_deref()))`, `Some("on".to_owned())`},
		{boolean("rush", "false", false), `single(&mut errors, "rush", boolean(self.rush.as_deref()))`, "None"},
		{boolean("paid", " True ", true), `single(&mut errors, "paid", yes_no(self.paid.as_deref().filter(|value| !value.is_empty()).or(Some("true"))))`, `Some("true".to_owned())`},
		{boolean("gifts", "false", true), `single(&mut errors, "gifts", yes_no(self.gifts.as_deref().filter(|value| !value.is_empty()).or(Some("false"))))`, `Some("false".to_owned())`},
	} {
		f := tc.field
		if got := f.ReadExpr(); got != tc.read {
			t.Errorf("%s: read %s, want %s", f.JSONName, got, tc.read)
		}
		if got := f.NewValue(); got != tc.newForm {
			t.Errorf("%s: new %s, want %s", f.JSONName, got, tc.newForm)
		}
		if f.Required {
			t.Errorf("%s: a boolean with a default is required", f.JSONName)
		}
	}
	if paid := boolean("paid", "true", true); len(paid.Options) != 2 || paid.Options[0].Value != "true" || paid.Options[1].Value != "false" {
		t.Errorf("a filter's boolean selects true or false: %+v", paid.Options)
	}
}

// TestArgumentsTheFormKeeps gives no argument form to an operation with an
// argument named as the form keeps a name: _action, which its input's row
// buttons send, and overflow, the form's own field; the reason says why.
func TestArgumentsTheFormKeeps(t *testing.T) {
	schemas := schemaSet{{}}
	rows := &formStruct{Name: "NoteInputForm", TypeName: "NoteInput", Rows: true}
	inputs := map[string]*formStruct{"NoteInput": rows}
	for _, tc := range []struct{ name, field, want string }{
		{"_action", "action", "argument _action and the name of its input's row buttons share a form field's name"},
		{"overflow", "overflow", "argument overflow is the form's overflow"},
	} {
		e := registry.RustEndpoint{
			Namespace: "notes",
			Name:      "add",
			Method:    "POST",
			ArgsName:  "NotesAddArgs",
			BodyArgs:  []registry.RustParam{{Name: tc.name, Field: tc.field, RustType: "Option<String>"}},
			Input:     &registry.RustInput{RustType: "types::NoteInput", Required: true},
		}
		op := &ir.FieldDef{Name: "add", Arguments: []*ir.ArgumentDef{{Name: tc.name, TypeRef: ir.TypeRef{Name: "string"}}}}
		_, reason, err := schemas.argForm(e, op, inputs)
		if err != nil || reason != tc.want {
			t.Errorf("%s: reason %q, err %v; want %q", tc.name, reason, err, tc.want)
		}
	}
}
