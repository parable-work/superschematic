package tsreader

import (
	"path/filepath"
	"testing"
)

// TestStrictJSONIsExplicitPerType: @strictJSON marks the decorated type only.
// A strict type's nested object type is strict because it is decorated too,
// not because it is reachable from a strict type.
func TestStrictJSONIsExplicitPerType(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "fixture-strict-json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Policy", "Grant"} {
		if !schema.Types[name].StrictJSON {
			t.Errorf("%s lost strictJSON", name)
		}
	}
	if schema.Types["Ordinary"].StrictJSON {
		t.Fatal("unmarked type became strict")
	}
}
