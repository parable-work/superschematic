package pygen

import "testing"

// The Python field type mapper wraps the element type once per list level.
// Depth 2 is exercised here only: the generator still refuses T[][] at its
// entry until it renders every part of a nested-array field.
func TestFieldTypeMapperPythonArrayDepth(t *testing.T) {
	cases := []struct {
		typeName   string
		arrayDepth int
		isMap      bool
		isRequired bool
		want       string
	}{
		{"string", 0, false, true, "str"},
		{"string", 1, false, true, "List[str]"},
		{"string", 2, false, true, "List[List[str]]"},
		{"Point", 2, false, false, "List[List[Point]]"},
		{"Point", 1, true, false, "Dict[str, List[Point | None]]"},
	}
	for _, tc := range cases {
		got := fieldTypeMapperPython(tc.typeName, tc.arrayDepth, tc.isMap, tc.isRequired, nil)
		if got != tc.want {
			t.Errorf("fieldTypeMapperPython(%q, %d, %v, %v) = %q, want %q", tc.typeName, tc.arrayDepth, tc.isMap, tc.isRequired, got, tc.want)
		}
	}
}
