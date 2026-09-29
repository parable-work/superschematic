package graphdesc_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	scalarlib "github.com/parable-work/superscalar/go"
)

// TestCanonicalValuesAreTheScalarCoresCanonicalForm reads the canonical-row
// vectors of the classes whose scalar core has a canonical form of its own
// (runtime/versiongraph/testdata/canonical) and checks each canonical value
// against superscalar, which the schema runtime's parse step calls: parsing
// it gives it back unchanged. A UUID's Postgres rendering, the hyphenated
// form, also parses to the vector's canonical value.
func TestCanonicalValuesAreTheScalarCoresCanonicalForm(t *testing.T) {
	for class, scalar := range map[string]string{
		"uuid":     "Identity.UUID",
		"dateTime": "Temporal.DateTime",
		"duration": "Temporal.Duration",
	} {
		text, err := os.ReadFile(filepath.Join("..", "..", "..", "runtime", "versiongraph", "testdata", "canonical", class+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Cases []struct {
				Name      string `json:"name"`
				Postgres  string `json:"postgres"`
				Canonical string `json:"canonical"`
			} `json:"cases"`
		}
		if err := json.Unmarshal(text, &doc); err != nil {
			t.Fatal(err)
		}
		checked := 0
		for _, c := range doc.Cases {
			if c.Canonical == "" || c.Canonical == "null" {
				continue
			}
			canonical, rendered := stringsOf(t, c.Canonical), stringsOf(t, c.Postgres)
			for i, value := range canonical {
				got, err := scalarlib.Parse(scalar, value)
				if err != nil || got != value {
					t.Errorf("%s %q: %s parses %q to %q, %v; the canonical form is its own parse", class, c.Name, scalar, value, got, err)
				}
				if class == "uuid" && len(rendered[i]) == 36 {
					if got, err := scalarlib.Parse(scalar, rendered[i]); err != nil || got != value {
						t.Errorf("%s %q: %s parses %q to %q, %v, want %q", class, c.Name, scalar, rendered[i], got, err, value)
					}
				}
				checked++
			}
		}
		if checked == 0 {
			t.Errorf("no %s vector has a canonical value", class)
		}
	}
}

// stringsOf flattens a JSON string, list of strings or list of lists of
// strings into its strings, in order.
func stringsOf(t *testing.T, text string) []string {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			out = append(out, v)
		case []any:
			for _, item := range v {
				walk(item)
			}
		default:
			t.Fatalf("%s holds %v, not strings", text, v)
		}
	}
	walk(value)
	return out
}
