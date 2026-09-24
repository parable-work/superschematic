package runtime

import (
	"testing"

	"github.com/parable-work/superschematic/ir"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nestedSchema declares arrays of arrays (T[][]) of a scalar, a builtin, an
// enum and an object type with a secret field.
func nestedSchema() *ir.Schema {
	s := ir.NewSchema("nested-test", ir.SchemaKindGeneral)
	s.Scalars["Name"] = &ir.ScalarDef{Name: "Name", Primitive: "String", MinLength: 2}
	s.Enums["Shade"] = &ir.EnumDef{Name: "Shade", Values: []ir.EnumValueDef{
		{Name: "Light", SerializedAs: "light"}, {Name: "Dark", SerializedAs: "dark"},
	}}
	nested := func(name string) ir.TypeRef {
		return ir.TypeRef{Name: name, IsArray: true, IsArrayOfArrays: true}
	}
	s.Types["Cell"] = &ir.TypeDef{Name: "Cell", Kind: ir.TypeKindObject, Fields: []*ir.FieldDef{
		{Name: "label", TypeRef: ir.TypeRef{Name: "String"}, Required: true},
		{Name: "token", TypeRef: ir.TypeRef{Name: "String"}, Secret: true},
	}}
	s.Types["Grid"] = &ir.TypeDef{Name: "Grid", Kind: ir.TypeKindObject, Fields: []*ir.FieldDef{
		{Name: "labels", TypeRef: nested("Name"), Required: true},
		{Name: "counts", TypeRef: nested("Int")},
		{Name: "shades", TypeRef: nested("Shade")},
		{Name: "cells", TypeRef: nested("Cell")},
	}}
	return s
}

// errorValidators maps each reported path to its first validator.
func errorValidators(errs ValidationErrors) map[string]string {
	got := map[string]string{}
	for key := range errs {
		if fieldErrs := errs.GetFieldErrors(key); len(fieldErrs) > 0 {
			got[key] = fieldErrs[0].Validator
		} else {
			got[key] = "nested"
		}
	}
	return got
}

func TestNestedArrays_LoadType(t *testing.T) {
	rt := New(nestedSchema())
	cases := []struct {
		name    string
		payload string
		want    map[string]string
	}{
		{"ragged rows", `{"labels":[["ab","cd"],[],["ef"]],"counts":[[1,2.0],[]],"shades":[["light"],["dark","light"]],"cells":[[{"label":"x"}],[]]}`, map[string]string{}},
		{"empty outer lists", `{"labels":[["ab"]],"counts":[],"shades":[],"cells":[]}`, map[string]string{}},
		{"empty inner lists", `{"labels":[[]],"counts":[[]],"shades":[[],[]],"cells":[[]]}`, map[string]string{}},
		{"null inner lists", `{"labels":[["ab"],null],"counts":[null],"cells":[[],null]}`,
			map[string]string{"labels[1]": "required", "counts[0]": "required", "cells[1]": "required"}},
		{"bad elements", `{"labels":[["ab"],["cd","x"]],"shades":[["light","purple"]],"cells":[[{"label":"a"},{}]]}`,
			map[string]string{"labels[1][1]": "minLength", "shades[0][1]": "enum", "cells[0][1]": "nested"}},
		{"inner value not a list", `{"labels":["ab"]}`, map[string]string{"labels[0]": "type"}},
		{"bad element type", `{"labels":[["ab"]],"counts":[[1],[2,"two"]]}`, map[string]string{"counts[1][1]": "type"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := rt.LoadType("Grid", []byte(tc.payload))
			assert.Equal(t, tc.want, errorValidators(errs))
		})
	}

	data, errs := rt.LoadType("Grid", []byte(`{"labels":[["ab"]],"counts":[[1,2.0],[]]}`))
	require.False(t, errs.HasErrors(), "errs=%v", errs)
	assert.Equal(t, []any{[]any{int64(1), int64(2)}, []any{}}, data["counts"], "inner elements coerced; empty inner list kept")

	_, nestedErrs := rt.LoadType("Grid", []byte(`{"labels":[["ab"]],"cells":[[],[{"label":"a"},{}]]}`))
	cellErrs := nestedErrs.GetNestedErrors("cells[1][1]")
	require.NotNil(t, cellErrs, "errs=%v", nestedErrs)
	assert.Equal(t, "required", cellErrs.GetFieldErrors("label")[0].Validator)
}

func TestNestedArrays_MaskMergeMarshal(t *testing.T) {
	rt := New(nestedSchema())
	data := map[string]any{
		"labels": []any{[]any{"ab"}, nil},
		"cells": []any{
			[]any{map[string]any{"label": "a", "token": "secret-a"}},
			nil,
			[]any{},
		},
	}

	masked := rt.MaskType("Grid", data)
	assert.Equal(t, []any{[]any{map[string]any{"label": "a", "token": nil}}, nil, []any{}}, masked["cells"])
	assert.Equal(t, "secret-a", data["cells"].([]any)[0].([]any)[0].(map[string]any)["token"], "source untouched")

	incoming := map[string]any{
		"labels": []any{[]any{"ab"}},
		"cells":  []any{[]any{map[string]any{"label": "b", "token": ""}, map[string]any{"label": "c", "token": ""}}},
	}
	merged := rt.MergeType("Grid", incoming, data)
	assert.Equal(t, []any{[]any{
		map[string]any{"label": "b", "token": "secret-a"},
		map[string]any{"label": "c", "token": ""},
	}}, merged["cells"], "secret kept by [i][j]; no existing element at [0][1]")

	encoded, errs := rt.MarshalType("Grid", data)
	require.False(t, errs.HasErrors(), "errs=%v", errs)
	assert.Equal(t, `{"labels":[["ab"],[]],"cells":[[{"label":"a","token":"secret-a"}],[],[]]}`, string(encoded), "declaration order; a nil inner list writes []")

	asMap, errs := rt.TypeToMap("Grid", data)
	require.False(t, errs.HasErrors(), "errs=%v", errs)
	assert.Equal(t, []any{[]any{"ab"}, []any{}}, asMap["labels"])
}
