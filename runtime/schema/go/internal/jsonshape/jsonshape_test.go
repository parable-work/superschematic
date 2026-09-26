package jsonshape

import (
	"testing"

	"github.com/parable-work/superschematic/ir"
)

func TestOf(t *testing.T) {
	cases := []struct {
		value any
		want  string
	}{
		{map[string]any{"k": "v"}, ir.JSONSchemaObjectType},
		{map[string]string{}, ir.JSONSchemaObjectType},
		{[]any{1.0}, ir.JSONSchemaArrayType},
		{[]float32{0.5}, ir.JSONSchemaArrayType},
		{[2]int{1, 2}, ir.JSONSchemaArrayType},
		{[]byte("[1]"), ""},
		{map[int]string{}, ""},
		{"[1]", ""},
		{1.5, ""},
		{true, ""},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := Of(tc.value); got != tc.want {
			t.Errorf("Of(%#v) = %q, want %q", tc.value, got, tc.want)
		}
	}
}
