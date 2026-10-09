package topcoat

import (
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// TestArgumentControls gives each argument the control its type and place
// call for: an input form's control for its type, a group for a list of an
// enum, an input per value for any other list, a hidden input for a path
// argument, and JSON text for a value no control holds.
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
		{name: "all", ref: ir.TypeRef{Name: "boolean"}, rustType: "Option<bool>", location: "query", filter: true, kind: kindValue, control: "checkbox", read: "flag"},
		{name: "notify", ref: ir.TypeRef{Name: "boolean"}, rustType: "Option<bool>", location: "body", kind: kindValue, control: "checkbox", read: "boolean"},
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
