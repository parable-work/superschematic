package typegen

import "testing"

// The Go field type mapper wraps the element type once per list level. The
// elements of a list are value types at every depth; only a map value keeps
// a pointer for a nullable element.
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
