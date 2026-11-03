package codegen

import (
	"sort"

	ir "github.com/parable-work/superschematic/ir"
)

// IsEngineType reports whether td, a type of a DB schema, is an engine
// type: one that composes a type-anchored behavior (D58,
// ir.TypeDef.IsEngineType). Its instances are the engine's, kept in the
// engine's own tables of the DB service's database, so it gets no table of
// its own, no repository and no migration. A type whose behaviors are all
// field-anchored stays a table (D103).
func IsEngineType(td *ir.TypeDef) bool {
	return td.IsEngineType()
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

// WithoutEngineTypes returns schema without its engine types: what the
// sql and orm generators, and the migration models built on the sql
// generator, see of a DB schema (D58). It returns schema itself when it
// has none, and otherwise a shallow copy whose Types map leaves them out;
// the type definitions are shared. The build refuses a table that
// references an engine type before any generator runs, so no table of the
// copy names a type it lacks.
func WithoutEngineTypes(schema *ir.Schema) *ir.Schema {
	if len(EngineTypes(schema)) == 0 {
		return schema
	}
	copied := *schema
	copied.Types = make(map[string]*ir.TypeDef, len(schema.Types))
	for name, td := range schema.Types {
		if !IsEngineType(td) {
			copied.Types[name] = td
		}
	}
	return &copied
}
