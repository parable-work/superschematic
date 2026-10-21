package ir

import (
	"reflect"
	"strings"
	"testing"
)

func TestValidateUIControl(t *testing.T) {
	for _, control := range UIControls {
		if err := ValidateUIControl(control); err != nil {
			t.Errorf("ValidateUIControl(%q) = %v", control, err)
		}
	}
	if err := ValidateUIControl("dial"); err == nil {
		t.Error(`ValidateUIControl("dial") accepted an unknown control`)
	}
}

// TestSchemaValidateRefusesUnknownUIControl: a data-form schema, which has
// no TypeScript type check, is held to UIControls too.
func TestSchemaValidateRefusesUnknownUIControl(t *testing.T) {
	schema := NewSchema("settings", SchemaKindGeneral)
	schema.Types["Settings"] = &TypeDef{Name: "Settings", Role: RoleEmbeddedStruct, Fields: []*FieldDef{
		{Name: "level", TypeRef: TypeRef{Name: "number"}, UIControl: "slider"},
		{Name: "mode", TypeRef: TypeRef{Name: "String"}, UIControl: "dial"},
	}}
	var got []string
	for _, err := range schema.Validate() {
		if strings.Contains(err.Error(), "@uiControl") {
			got = append(got, err.Error())
		}
	}
	want := []string{`Settings.mode has an invalid @uiControl: unknown control "dial"; use one of ` + strings.Join(UIControls, ", ")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Validate = %q, want %q", got, want)
	}
}
