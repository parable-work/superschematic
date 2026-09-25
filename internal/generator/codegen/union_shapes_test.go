package codegen

import (
	"reflect"
	"testing"
)

// TestUnionShapesTags: a tag is a single string or enum field that every
// member declaring it gives a distinct default, and at least two members
// declare. Anything else is only a field name.
func TestUnionShapesTags(t *testing.T) {
	value := func(text string) *string { return &text }
	isEnum := func(typeName string) bool { return typeName == "Kind" }
	members := [][]FieldInfo{
		{
			{Name: "kind", Type: "Kind", Default: value("a")},
			{Name: "label", Type: PrimitiveString, Default: value("x")},
			{Name: "mode", Type: PrimitiveString, Default: value("same")},
			{Name: "tags", Type: PrimitiveString, IsArray: true, Default: value("[]")},
			{Name: "count", Type: PrimitiveNumber, Default: value("1")},
			{Name: "only", Type: PrimitiveString, Default: value("solo")},
		},
		{
			{Name: "kind", Type: "Kind", Default: value("b")},
			{Name: "label", Type: PrimitiveString},
			{Name: "mode", Type: PrimitiveString, Default: value("same")},
			{Name: "tags", Type: PrimitiveString, IsArray: true, Default: value("[\"b\"]")},
			{Name: "count", Type: PrimitiveNumber, Default: value("2")},
		},
		{
			{Name: "other", Type: PrimitiveString},
		},
	}
	want := []UnionShape{
		{Fields: []string{"kind", "label", "mode", "tags", "count", "only"}, Tags: []UnionTag{{Field: "kind", Value: "a"}}},
		{Fields: []string{"kind", "label", "mode", "tags", "count"}, Tags: []UnionTag{{Field: "kind", Value: "b"}}},
		{Fields: []string{"other"}},
	}
	if got := UnionShapes(members, isEnum); !reflect.DeepEqual(got, want) {
		t.Fatalf("UnionShapes = %+v, want %+v", got, want)
	}
}
