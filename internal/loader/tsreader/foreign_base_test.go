package tsreader

import (
	"path/filepath"
	"slices"
	"testing"
)

// TestForeignBaseRecordsImports pins that a class extending a class from
// another service records, as imports, the types the flattened base fields
// reference. Those fields become this schema's own fields, so its generated
// code refers to the base service's StampStage and StampOrigin.
func TestForeignBaseRecordsImports(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "fixture-foreign-base"))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	job := schema.Types["Job"]
	if job == nil {
		t.Fatal("Job was not loaded")
	}
	var inherited []string
	for _, field := range job.Fields {
		if field.InheritedFrom == "Stamped" {
			inherited = append(inherited, field.Name+":"+field.TypeRef.Name)
		}
	}
	if !slices.Equal(inherited, []string{"stage:StampStage", "origin:StampOrigin"}) {
		t.Fatalf("fields inherited from Stamped = %v", inherited)
	}
	var imported []string
	for _, imp := range schema.Imports {
		if imp.Package == "@schemas/fixture-base-lib" {
			imported = imp.Types
		}
	}
	if !slices.Equal(imported, []string{"StampOrigin", "StampStage"}) {
		t.Fatalf("imports from @schemas/fixture-base-lib = %v, want StampOrigin and StampStage (all imports: %+v)", imported, schema.Imports)
	}
}
