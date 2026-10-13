package codegen

import (
	"sort"

	ir "github.com/parable-work/superschematic/ir"
)

// IsEngineType reports whether td, a type of a DB schema, is an engine
// type: one that composes a behavior (D58). Its instances are the
// engine's, kept in the engine's own tables of the DB service's database,
// so it gets no table of its own, no repository and no migration.
func IsEngineType(td *ir.TypeDef) bool {
	return td != nil && len(td.Behaviors) > 0
}

// EngineTypes returns the engine types of a DB schema, sorted by name.
func EngineTypes(schema *ir.Schema) []*ir.TypeDef {
	if schema == nil {
		return nil
	}
	var types []*ir.TypeDef
	for _, td := range schema.Types {
		if IsEngineType(td) {
			types = append(types, td)
		}
	}
	sort.Slice(types, func(i, j int) bool { return types[i].Name < types[j].Name })
	return types
}
