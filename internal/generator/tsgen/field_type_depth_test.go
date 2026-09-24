package tsgen

import "testing"

// The TS field type mapper wraps the element type once per list level.
func TestFieldTypeMapperTSArrayDepth(t *testing.T) {
	cases := []struct {
		typeName   string
		arrayDepth int
		isMap      bool
		isRequired bool
		want       string
	}{
		{"string", 0, false, true, "string"},
		{"string", 1, false, true, "string[]"},
		{"string", 2, false, true, "string[][]"},
		{"Point", 2, false, false, "Point[][]"},
		{"Point", 1, true, false, "Record<string, (Point | null)[]>"},
	}
	for _, tc := range cases {
		got := fieldTypeMapperTS(tc.typeName, tc.arrayDepth, tc.isMap, tc.isRequired, nil)
		if got != tc.want {
			t.Errorf("fieldTypeMapperTS(%q, %d, %v, %v) = %q, want %q", tc.typeName, tc.arrayDepth, tc.isMap, tc.isRequired, got, tc.want)
		}
	}
}
