package rustgen

import "testing"

// TestQualifyRustType: a name the types crate declares is qualified with
// the prefix wherever it stands, and a path to another crate is not.
func TestQualifyRustType(t *testing.T) {
	local := map[string]bool{"Category": true, "IdentityUUID": true, "DateTime": true}
	for in, want := range map[string]string{
		"Category":                       "db::Category",
		"Vec<Category>":                  "Vec<db::Category>",
		"HashMap<String, IdentityUUID>":  "HashMap<String, db::IdentityUUID>",
		"superscalar::DateTime":          "superscalar::DateTime",
		"Option<Vec<superscalar::Uuid>>": "Option<Vec<superscalar::Uuid>>",
		"String":                         "String",
	} {
		if got := qualifyRustType(in, local, "db::"); got != want {
			t.Errorf("qualifyRustType(%q) = %q, want %q", in, got, want)
		}
	}
}
