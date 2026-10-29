package codegen

import (
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestDefaultNowIsNoDecodersDefault: Default<T, "now"> on a date, time or
// timestamp is the database's (D88), so no generator renders it as a
// literal and a payload still holds the field; on text, "now" is a string.
func TestDefaultNowIsNoDecodersDefault(t *testing.T) {
	now := DefaultNow
	for _, name := range []string{"Temporal.DateTime", "Temporal.Date", "Temporal.Time"} {
		if kind := ClassifyDefault(FieldInfo{Type: name, Default: &now}, nil); kind != DefaultLiteralUnknown {
			t.Errorf("%s: ClassifyDefault = %v, want none", name, kind)
		}
		if !ReadRequired(&ir.FieldDef{Name: "at", TypeRef: ir.TypeRef{Name: name}, Required: true, Default: &now}) {
			t.Errorf("%s: a payload may leave out a field the database defaults", name)
		}
	}
	if kind := ClassifyDefault(FieldInfo{Type: "string", Default: &now}, nil); kind != DefaultLiteralString {
		t.Errorf("string: ClassifyDefault = %v, want a string", kind)
	}
	if ReadRequired(&ir.FieldDef{Name: "label", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Default: &now}) {
		t.Error("a defaulted string is read required")
	}
}
