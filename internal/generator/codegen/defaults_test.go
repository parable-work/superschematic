package codegen

import "testing"

func TestClassifyDefaultEmptyArray(t *testing.T) {
	value := "[]"
	field := FieldInfo{
		Type:     "Widget",
		IsArray:  true,
		Required: true,
		Default:  &value,
	}
	if got := ClassifyDefault(field, nil); got != DefaultLiteralEmptyArray {
		t.Fatalf("ClassifyDefault(empty array) = %v, want DefaultLiteralEmptyArray", got)
	}
}
