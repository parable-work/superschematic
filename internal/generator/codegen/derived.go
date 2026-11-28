package codegen

import (
	"sort"

	ir "github.com/parable-work/superschematic/ir"
)

// DerivedTableTypes returns the tables of a DB schema that implement
// Derived (D103), sorted by name. A deriver fills their derived fields
// through their repositories (D146).
func DerivedTableTypes(schema *ir.Schema) []*ir.TypeDef {
	if schema == nil {
		return nil
	}
	var types []*ir.TypeDef
	for _, td := range schema.Types {
		if td.ComposesDerived() {
			types = append(types, td)
		}
	}
	sort.Slice(types, func(i, j int) bool { return types[i].Name < types[j].Name })
	return types
}

// DerivedTables returns the names of the tables of a DB schema that
// implement Derived, sorted.
func DerivedTables(schema *ir.Schema) []string {
	var names []string
	for _, td := range DerivedTableTypes(schema) {
		names = append(names, td.Name)
	}
	return names
}
