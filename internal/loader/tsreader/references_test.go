package tsreader

import (
	"slices"
	"testing"
)

// visitHandles finds each handle in an evaluated argument with the object
// keys that lead to it, reading a list element by element, and an identity
// path covers what lies under it (D41).
func TestHandlePathsAndIdentityPaths(t *testing.T) {
	db := serviceHandle{name: "db", kind: "DB"}
	api := serviceHandle{name: "api", kind: "API"}
	arg := map[string]any{
		"deploy":   []any{api},
		"settings": []any{map[string]any{"of": db, "tier": "small"}, map[string]any{"of": map[string]any{"class": "Backend"}}},
		"from":     []any{api, map[string]any{"via": db}},
		"region":   "eu",
	}
	var paths []string
	visitHandles(arg, "", func(at string, handle serviceHandle) {
		paths = append(paths, at+"="+handle.name)
	})
	slices.Sort(paths)
	if want := []string{"deploy=api", "from.via=db", "from=api", "settings.of=db"}; !slices.Equal(paths, want) {
		t.Errorf("handles = %v, want %v", paths, want)
	}
	var whole []string
	visitHandles(db, "", func(at string, _ serviceHandle) { whole = append(whole, at) })
	if !slices.Equal(whole, []string{""}) {
		t.Errorf("a whole-argument handle is at %q, want \"\"", whole)
	}

	for _, tc := range []struct {
		paths []string
		at    string
		want  bool
	}{
		{[]string{"from"}, "from", true},
		{[]string{"from"}, "from.via", true},
		{[]string{"from"}, "fromage", false},
		{[]string{"from"}, "deploy", false},
		{[]string{"settings.of"}, "settings.of", true},
		{[]string{"settings.of"}, "settings", false},
		{[]string{""}, "deploy", true},
		{nil, "", false},
	} {
		if got := underIdentityPath(tc.paths, tc.at); got != tc.want {
			t.Errorf("underIdentityPath(%q, %q) = %v, want %v", tc.paths, tc.at, got, tc.want)
		}
	}
}
