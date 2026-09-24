package rustgen

import "testing"

// The Rust field type mapper wraps the element type once per list level.
// Depth 2 is exercised here only: the generator still refuses T[][] at its
// entry until it renders every part of a nested-array field.
func TestFieldTypeMapperRustArrayDepth(t *testing.T) {
	cases := []struct {
		typeName   string
		arrayDepth int
		isMap      bool
		isRequired bool
		want       string
	}{
		{"string", 0, false, true, "String"},
		{"string", 1, false, true, "Vec<String>"},
		{"string", 2, false, true, "Vec<Vec<String>>"},
		{"Point", 2, false, false, "Vec<Vec<Point>>"},
		{"Point", 1, true, false, "HashMap<String, Vec<Option<Point>>>"},
	}
	for _, tc := range cases {
		got := fieldTypeMapperRust(tc.typeName, tc.arrayDepth, tc.isMap, tc.isRequired, nil)
		if got != tc.want {
			t.Errorf("fieldTypeMapperRust(%q, %d, %v, %v) = %q, want %q", tc.typeName, tc.arrayDepth, tc.isMap, tc.isRequired, got, tc.want)
		}
	}
}
