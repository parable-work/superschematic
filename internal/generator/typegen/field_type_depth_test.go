package typegen

import "testing"

// The Go field type mapper wraps the element type once per list level.
// Depth 2 is exercised here only: the generator still refuses T[][] at its
// entry until it renders every part of a nested-array field.
func TestFieldTypeMapperGoArrayDepth(t *testing.T) {
	cases := []struct {
		typeName   string
		arrayDepth int
		isMap      bool
		isRequired bool
		want       string
	}{
		{"string", 0, false, true, "string"},
		{"string", 1, false, true, "[]string"},
		{"string", 2, false, true, "[][]string"},
		{"Point", 0, false, false, "*Point"},
		{"Point", 2, false, false, "[][]Point"},
		{"string", 1, true, false, "map[string][]*string"},
	}
	for _, tc := range cases {
		got := fieldTypeMapperGo(tc.typeName, tc.arrayDepth, tc.isMap, tc.isRequired, nil)
		if got != tc.want {
			t.Errorf("fieldTypeMapperGo(%q, %d, %v, %v) = %q, want %q", tc.typeName, tc.arrayDepth, tc.isMap, tc.isRequired, got, tc.want)
		}
	}
}
