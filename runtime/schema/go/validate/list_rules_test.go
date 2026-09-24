package validate

import (
	"testing"

	"github.com/parable-work/superschematic/ir"
	"github.com/stretchr/testify/assert"
)

// listRulesSchema declares T[] and T[][] fields of a builtin and of an inline
// scalar, each with list bounds and element constraints.
func listRulesSchema() *ir.Schema {
	one, three, two, four := 1, 3, 2, 4
	s := ir.NewSchema("list-rules", ir.SchemaKindGeneral)
	s.Scalars["Code"] = &ir.ScalarDef{Name: "Code", Primitive: "String"}
	list := ir.TypeRef{Name: "String", IsArray: true}
	grid := ir.TypeRef{Name: "String", IsArray: true, IsArrayOfArrays: true}
	codes := ir.TypeRef{Name: "Code", IsArray: true}
	codeGrid := ir.TypeRef{Name: "Code", IsArray: true, IsArrayOfArrays: true}
	s.Types["Rules"] = &ir.TypeDef{Name: "Rules", Kind: ir.TypeKindObject, Fields: []*ir.FieldDef{
		{Name: "tags", TypeRef: list, Required: true, ValidateListMin: &one, ValidateListMax: &three, ValidateMaxLength: &four},
		{Name: "rows", TypeRef: grid, Required: true},
		{Name: "cells", TypeRef: grid, ValidateListMin: &one, ValidateListMax: &two, ValidateMinLength: &two, ValidatePattern: "^[a-z]+$"},
		{Name: "codes", TypeRef: codes, ValidateMaxLength: &four},
		{Name: "codeGrid", TypeRef: codeGrid, ValidateMaxLength: &four},
	}}
	return s
}

func validatorsByPath(errs ValidationErrors) map[string][]string {
	got := map[string][]string{}
	for key := range errs {
		for _, fieldErr := range errs.GetFieldErrors(key) {
			got[key] = append(got[key], fieldErr.Validator)
		}
	}
	return got
}

// TestListRules pins the list rules for T[] and T[][]: a required list means
// present, not non-empty; listMin and listMax bound the outer list; a
// field's own constraints apply to every element and every innermost
// element; an element and an inner list are never null; a non-list inner
// value is a type error.
func TestListRules(t *testing.T) {
	v := New(listRulesSchema())
	base := func(extra map[string]any) map[string]any {
		data := map[string]any{"tags": []any{"a"}, "rows": []any{}}
		for k, val := range extra {
			data[k] = val
		}
		return data
	}
	cases := []struct {
		name string
		data map[string]any
		want map[string][]string
	}{
		{"required lists present and empty", map[string]any{"tags": []any{}, "rows": []any{}}, map[string][]string{"tags": {"listMin"}}},
		{"required list of lists present and empty", base(nil), map[string][]string{}},
		{"required lists absent", map[string]any{}, map[string][]string{"tags": {"required"}, "rows": {"required"}}},
		{"listMax bounds the list", base(map[string]any{"tags": []any{"a", "b", "c", "d"}}), map[string][]string{"tags": {"listMax"}}},
		{"listMin and listMax bound the outer list", base(map[string]any{"cells": []any{}}), map[string][]string{"cells": {"listMin"}}},
		{"outer list over listMax", base(map[string]any{"cells": []any{[]any{}, []any{}, []any{}}}), map[string][]string{"cells": {"listMax"}}},
		{"inner lists are unbounded", base(map[string]any{"cells": []any{[]any{"ab", "cd", "ef", "gh"}}}), map[string][]string{}},
		{"element constraints on T[]", base(map[string]any{"tags": []any{"ok", "toolong"}}), map[string][]string{"tags[1]": {"maxLength"}}},
		{"element constraints on T[][]", base(map[string]any{"cells": []any{[]any{"ab"}, []any{"x", "AB"}}}),
			map[string][]string{"cells[1][0]": {"minLength"}, "cells[1][1]": {"pattern"}}},
		{"element constraints on scalar elements", base(map[string]any{"codes": []any{"abcde"}, "codeGrid": []any{[]any{"ab", "abcde"}}}),
			map[string][]string{"codes[0]": {"maxLength"}, "codeGrid[0][1]": {"maxLength"}}},
		{"null elements", base(map[string]any{"tags": []any{"a", nil}, "codes": []any{nil}, "codeGrid": []any{[]any{nil}}}),
			map[string][]string{"tags[1]": {"required"}, "codes[0]": {"required"}, "codeGrid[0][0]": {"required"}}},
		{"null and non-list inner lists", base(map[string]any{"rows": []any{nil, "a", []any{}}}),
			map[string][]string{"rows[0]": {"required"}, "rows[1]": {"type"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, validatorsByPath(v.ValidateType("Rules", tc.data)))
		})
	}

	errs := v.ValidateType("Rules", base(map[string]any{"rows": []any{nil, 7}, "cells": []any{}}))
	assert.Equal(t, []ValidationError{{Validator: "required", Message: "required field"}}, errs.GetFieldErrors("rows[0]"))
	assert.Equal(t, []ValidationError{{Validator: "type", Message: "expected an array"}}, errs.GetFieldErrors("rows[1]"))
	assert.Equal(t, []ValidationError{{Validator: "listMin", Message: "must contain at least 1 items"}}, errs.GetFieldErrors("cells"))
}
