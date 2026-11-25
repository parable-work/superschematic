package tsreader

import (
	"path/filepath"
	"testing"
)

// TestMergeableTraitParses: the reader records `implements Mergeable`, the
// core trait @superschematic/db exports, as TypeDef.Mergeable (D147), and
// as no TraitRef and no raw heritage, as it records the user model's
// traits; a table that does not implement it is not marked.
func TestMergeableTraitParses(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "fixture-merge-db"))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	for name, want := range map[string]bool{
		"Label": true, "Customer": true,
		"Document": false, "DocumentLabel": false, "Subscription": false, "LabelNote": false, "LabelCode": false, "CustomerProfile": false, "Purchase": false,
	} {
		td := schema.Types[name]
		if td == nil {
			t.Fatalf("%s not loaded", name)
		}
		if td.Mergeable != want {
			t.Errorf("%s.Mergeable = %t, want %t", name, td.Mergeable, want)
		}
		if len(td.Implements) > 0 || td.RawHeritage != nil {
			t.Errorf("%s records heritage (%v, %+v); Mergeable is core IR, not a schema trait", name, td.Implements, td.RawHeritage)
		}
	}
}
