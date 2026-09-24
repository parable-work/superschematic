package codegen

import (
	"encoding/json"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// depthRecordingMapper renders a Go-style type and records the arguments
// ExtractFieldInfo passed it.
type depthRecordingMapper struct {
	typeName   string
	arrayDepth int
	isMap      bool
	isRequired bool
}

func (m *depthRecordingMapper) mapType(typeName string, arrayDepth int, isMap bool, isRequired bool, _ ScalarMap) string {
	m.typeName, m.arrayDepth, m.isMap, m.isRequired = typeName, arrayDepth, isMap, isRequired
	valueType := WrapArray(typeName, arrayDepth, func(elem string) string { return "[]" + elem })
	if isMap {
		return "map[string]" + valueType
	}
	return valueType
}

func TestExtractFieldInfoThreadsArrayDepth(t *testing.T) {
	cases := []struct {
		name                string
		typeRef             ir.TypeRef
		wantDepth           int
		wantIsArray         bool
		wantIsArrayOfArrays bool
		wantTargetType      string
	}{
		{"scalar", ir.TypeRef{Name: "string"}, 0, false, false, "string"},
		{"list", ir.TypeRef{Name: "string", IsArray: true}, 1, true, false, "[]string"},
		{"list of lists", ir.TypeRef{Name: "string", IsArray: true, IsArrayOfArrays: true}, 2, true, true, "[][]string"},
		{"map", ir.TypeRef{Name: "string", IsMap: true}, 0, false, false, "map[string]string"},
		{"map of lists", ir.TypeRef{Name: "string", IsArray: true, IsMap: true}, 1, true, false, "map[string][]string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapper := &depthRecordingMapper{}
			field := &ir.FieldDef{Name: "cells", TypeRef: tc.typeRef, Required: true}
			info := ExtractFieldInfo(field, ScalarMap{}, nil, ExtractionConfig{FieldTypeMapper: mapper.mapType})

			if mapper.typeName != "string" || mapper.arrayDepth != tc.wantDepth || mapper.isMap != tc.typeRef.IsMap || !mapper.isRequired {
				t.Errorf("mapper got (%q, %d, %v, %v), want (string, %d, %v, true)",
					mapper.typeName, mapper.arrayDepth, mapper.isMap, mapper.isRequired, tc.wantDepth, tc.typeRef.IsMap)
			}
			if info.TargetType != tc.wantTargetType {
				t.Errorf("TargetType = %q, want %q", info.TargetType, tc.wantTargetType)
			}
			if info.IsArray != tc.wantIsArray || info.IsArrayOfArrays != tc.wantIsArrayOfArrays || info.IsMap != tc.typeRef.IsMap {
				t.Errorf("flags IsArray=%v IsArrayOfArrays=%v IsMap=%v, want %v %v %v",
					info.IsArray, info.IsArrayOfArrays, info.IsMap, tc.wantIsArray, tc.wantIsArrayOfArrays, tc.typeRef.IsMap)
			}
			if info.ArrayDepth() != tc.wantDepth {
				t.Errorf("ArrayDepth() = %d, want %d", info.ArrayDepth(), tc.wantDepth)
			}
		})
	}
}

func TestWrapArrayPerLanguage(t *testing.T) {
	wrappers := []struct {
		language string
		elem     string
		wrap     func(string) string
		want     [3]string
	}{
		{"go", "string", func(e string) string { return "[]" + e }, [3]string{"string", "[]string", "[][]string"}},
		{"typescript", "string", func(e string) string { return e + "[]" }, [3]string{"string", "string[]", "string[][]"}},
		{"python", "str", func(e string) string { return "List[" + e + "]" }, [3]string{"str", "List[str]", "List[List[str]]"}},
		{"rust", "String", func(e string) string { return "Vec<" + e + ">" }, [3]string{"String", "Vec<String>", "Vec<Vec<String>>"}},
	}
	for _, w := range wrappers {
		for depth, want := range w.want {
			if got := WrapArray(w.elem, depth, w.wrap); got != want {
				t.Errorf("%s depth %d = %q, want %q", w.language, depth, got, want)
			}
		}
	}
	if got := WrapArray("string", -1, func(e string) string { return "[]" + e }); got != "string" {
		t.Errorf("negative depth = %q, want the element unchanged", got)
	}
}

func TestWrapJSONSchemaArray(t *testing.T) {
	cases := []struct {
		depth int
		want  string
	}{
		{0, `{"format":"uuid","type":"string"}`},
		{1, `{"items":{"format":"uuid","type":"string"},"type":"array"}`},
		{2, `{"items":{"items":{"format":"uuid","type":"string"},"type":"array"},"type":"array"}`},
	}
	for _, tc := range cases {
		item := map[string]interface{}{"type": "string", "format": "uuid"}
		got, err := json.Marshal(WrapJSONSchemaArray(item, tc.depth))
		if err != nil {
			t.Fatalf("depth %d: %v", tc.depth, err)
		}
		if string(got) != tc.want {
			t.Errorf("depth %d = %s, want %s", tc.depth, got, tc.want)
		}
	}

	// The item schema is referenced, not copied: constraints set on it after
	// wrapping land on the innermost items.
	item := map[string]interface{}{"type": "string"}
	outer := WrapJSONSchemaArray(item, 2)
	item["maxLength"] = 3
	inner := outer["items"].(map[string]interface{})["items"].(map[string]interface{})
	if inner["maxLength"] != 3 {
		t.Errorf("innermost items = %v, want the wrapped item schema", inner)
	}
}

func TestFieldInfoArrayDepth(t *testing.T) {
	cases := []struct {
		field FieldInfo
		want  int
	}{
		{FieldInfo{}, 0},
		{FieldInfo{IsArray: true}, 1},
		{FieldInfo{IsArray: true, IsArrayOfArrays: true}, 2},
		{FieldInfo{IsArray: true, IsMap: true}, 1},
	}
	for i, tc := range cases {
		if got := tc.field.ArrayDepth(); got != tc.want {
			t.Errorf("case %d: ArrayDepth() = %d, want %d", i, got, tc.want)
		}
	}
}
