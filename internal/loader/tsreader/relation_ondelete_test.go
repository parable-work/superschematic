package tsreader

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// TestRelationOnDeleteParses: the loader reads the optional onDelete config from
// a Relation<T, { onDelete: ... }> field into RelationDef.OnDelete, and leaves a
// bare Relation<T> empty (the generator defaults empty to CASCADE).
func TestRelationOnDeleteParses(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "fixture-relation-ondelete"))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	child := schema.Types["Child"]
	if child == nil {
		t.Fatal("Child not loaded")
	}
	want := map[string]string{
		"parentBare":     "",
		"parentRestrict": "RESTRICT",
		"parentNoAction": "NO ACTION",
	}
	seen := map[string]bool{}
	for _, fd := range child.Fields {
		exp, ok := want[fd.Name]
		if !ok {
			continue
		}
		seen[fd.Name] = true
		if fd.Relation == nil {
			t.Fatalf("field %q: expected a Relation, got nil", fd.Name)
		}
		if fd.Relation.OnDelete != exp {
			t.Errorf("field %q: OnDelete = %q, want %q", fd.Name, fd.Relation.OnDelete, exp)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("field %q not found on Child", name)
		}
	}
}

// TestRelationOnDeleteInvalidValueFails: 'BOGUS' is outside the OnDeleteAction
// union, so TS rejects the constraint before the walk and the error names the
// bad value. Asserting on the value (not just errors.As) means a fixture with an
// unrelated typo cannot pass this test.
func TestRelationOnDeleteInvalidValueFails(t *testing.T) {
	_, _, err := LoadService(filepath.Join("testdata", "services", "broken-relation-ondelete"))
	if err == nil {
		t.Fatal("expected schema error for invalid onDelete value")
	}
	var list SchemaErrorList
	if !errors.As(err, &list) {
		t.Fatalf("expected SchemaErrorList, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "BOGUS") {
		t.Errorf("error must name the bad value BOGUS; got: %s", err.Error())
	}
}

// TestRelationOnDeleteUnknownKeyFails: a typo key ('onDlete') is not caught by
// TS excess-property checking in type-argument position, so it reaches the walk;
// the Go relationConfigFromTypeNode default branch is the sole guard. This
// message is Go-generated and fully under our control, so assert both the phrase
// and the key -- proving the Go unknown-key branch actually fired.
func TestRelationOnDeleteUnknownKeyFails(t *testing.T) {
	_, _, err := LoadService(filepath.Join("testdata", "services", "broken-relation-ondelete-unknownkey"))
	if err == nil {
		t.Fatal("expected schema error for unknown onDelete config key")
	}
	var list SchemaErrorList
	if !errors.As(err, &list) {
		t.Fatalf("expected SchemaErrorList, got %T: %v", err, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "unsupported Relation config key") || !strings.Contains(msg, "onDlete") {
		t.Errorf("error must name the unsupported key onDlete; got: %s", msg)
	}
}
