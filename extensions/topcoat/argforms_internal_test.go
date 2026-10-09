package topcoat

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// TestArgumentControls gives each argument the control its type and place
// call for, or the reason a form field cannot hold it.
func TestArgumentControls(t *testing.T) {
	schemas := schemaSet{{
		Enums: map[string]*ir.EnumDef{"Shade": {Name: "Shade", Values: []ir.EnumValueDef{{Name: "Light", SerializedAs: "light"}}}},
		Types: map[string]*ir.TypeDef{"Point": {Name: "Point"}},
		Scalars: map[string]*ir.ScalarDef{
			"Generic.JSON": {Name: "Generic.JSON", TypeMappings: map[string]string{"json_schema": "object"}},
		},
	}}
	maxLength := 4
	for _, tc := range []struct {
		name     string
		ref      ir.TypeRef
		rustType string
		location string
		filter   bool
		control  string
		read     string
		attrs    string
		reason   string
	}{
		{name: "id", ref: ir.TypeRef{Name: "string"}, rustType: "String", location: "path", control: "hidden", read: "text"},
		{name: "note", ref: ir.TypeRef{Name: "string"}, rustType: "Option<String>", location: "body", control: "text", read: "text", attrs: ` maxlength="4"`},
		{name: "shade", ref: ir.TypeRef{Name: "Shade"}, rustType: "types::Shade", location: "body", control: "select", read: "text", attrs: " required=(true)"},
		{name: "shades", ref: ir.TypeRef{Name: "Shade", IsArray: true}, rustType: "Option<Vec<types::Shade>>", location: "query", filter: true, control: "checkboxes", read: "text"},
		{name: "codes", ref: ir.TypeRef{Name: "string", IsArray: true}, rustType: "Vec<String>", location: "query", filter: true, control: "repeated", read: "text", attrs: ` maxlength="4"`},
		{name: "limit", ref: ir.TypeRef{Name: "number"}, rustType: "Option<f64>", location: "query", filter: true, control: "number", read: "number", attrs: ` step="any"`},
		{name: "all", ref: ir.TypeRef{Name: "boolean"}, rustType: "Option<bool>", location: "query", filter: true, control: "checkbox", read: "flag"},
		{name: "notify", ref: ir.TypeRef{Name: "boolean"}, rustType: "Option<bool>", location: "body", control: "checkbox", read: "text"},
		{name: "labels", ref: ir.TypeRef{Name: "string", IsMap: true}, rustType: "HashMap<String, String>", location: "body", reason: "argument labels is a map"},
		{name: "grid", ref: ir.TypeRef{Name: "number", IsArray: true, IsArrayOfArrays: true}, rustType: "Vec<Vec<f64>>", location: "body", reason: "is a list of lists"},
		{name: "at", ref: ir.TypeRef{Name: "Point"}, rustType: "types::Point", location: "body", reason: "is an object"},
		{name: "flags", ref: ir.TypeRef{Name: "boolean", IsArray: true}, rustType: "Vec<bool>", location: "body", reason: "is a list of booleans"},
		{name: "extra", ref: ir.TypeRef{Name: "Generic.JSON"}, rustType: "serde_json::Value", location: "body", reason: "any JSON value"},
	} {
		arg := &ir.ArgumentDef{Name: tc.name, TypeRef: tc.ref}
		if tc.name == "note" || tc.name == "codes" {
			arg.ValidateMaxLength = &maxLength
		}
		param := registry.RustParam{Name: tc.name, Field: tc.name, RustType: tc.rustType}
		field, reason := schemas.argField("op", arg, param, tc.location, tc.filter)
		if tc.reason != "" {
			if !strings.Contains(reason, tc.reason) {
				t.Errorf("%s: reason %q, want one with %q", tc.name, reason, tc.reason)
			}
			continue
		}
		if reason != "" || field.Control != tc.control || field.Read != tc.read || field.Attrs != tc.attrs {
			t.Errorf("%s: control %q read %q attrs %q (reason %q); want %q %q %q", tc.name, field.Control, field.Read, field.Attrs, reason, tc.control, tc.read, tc.attrs)
		}
	}
}
