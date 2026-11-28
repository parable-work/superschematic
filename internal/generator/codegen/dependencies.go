package codegen

import (
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// TypeDependencies returns, sorted, the dependency services whose types
// package the schema's type libraries import: the ones a Go module
// requires, a TypeScript package depends on, a Rust crate path-depends on
// and a Python package imports from. deps holds the loaded dependency
// schemas by service name; an import of a service missing from it is left
// to the generators, which fail on it.
//
// The classification is the one the four type generators apply to
// schema.Imports: a DB dependency contributes the symbols the schema names,
// any other kind its whole catalog, and a dependency is imported when one
// of those symbols is an enum, a union or a non-trait type that no local
// definition shadows. Scalars are regenerated locally and import nothing.
// The generators also skip a symbol an earlier dependency already
// provided, so they can import fewer dependencies than this returns, never
// more.
func TypeDependencies(schema *ir.Schema, deps map[string]*ir.Schema) []string {
	var names []string
	seen := make(map[string]bool)
	for _, imp := range schema.Imports {
		name := DependencyServiceName(imp.Package)
		dep := deps[name]
		if dep == nil || seen[name] || !importsTypes(schema, imp, dep) {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// importsTypes reports whether imp brings a generated type of dep into
// schema.
func importsTypes(schema *ir.Schema, imp ir.Import, dep *ir.Schema) bool {
	symbols := append([]string{}, imp.Types...)
	if dep.Kind != ir.SchemaKindDB {
		for name := range dep.Enums {
			symbols = append(symbols, name)
		}
		for name := range dep.Unions {
			symbols = append(symbols, name)
		}
		for name := range dep.Types {
			symbols = append(symbols, name)
		}
	}
	for _, symbol := range symbols {
		if schema.Enums[symbol] != nil || schema.Unions[symbol] != nil || schema.Types[symbol] != nil {
			continue
		}
		if dep.Scalars[symbol] != nil {
			continue
		}
		if dep.Enums[symbol] != nil || dep.Unions[symbol] != nil {
			return true
		}
		if typeDef := dep.Types[symbol]; typeDef != nil && !typeDef.IsTrait && typeDef.Role != ir.RoleTrait {
			return true
		}
	}
	return false
}

// DependencyServiceName extracts the service name from a schema import
// package (e.g. "@schemas/web-db" -> "web-db").
func DependencyServiceName(pkg string) string {
	if idx := strings.LastIndex(pkg, "/"); idx >= 0 {
		return pkg[idx+1:]
	}
	return pkg
}

// LocalSchemaSymbols returns the set of definition names owned by the schema
// itself: its enums, types and unions. A dependency symbol with the same
// name is shadowed and must not be re-exported.
func LocalSchemaSymbols(schema *ir.Schema) map[string]struct{} {
	symbols := make(map[string]struct{}, len(schema.Enums)+len(schema.Types)+len(schema.Unions))
	for name := range schema.Enums {
		symbols[name] = struct{}{}
	}
	for name := range schema.Types {
		symbols[name] = struct{}{}
	}
	for name := range schema.Unions {
		symbols[name] = struct{}{}
	}
	return symbols
}

// ImportSymbols returns the symbols a schema imports from a dependency, in
// no particular order: a DB dependency contributes the symbols the import
// names, any other kind those and its whole catalog.
func ImportSymbols(imp ir.Import, depSchema *ir.Schema) []string {
	if depSchema.Kind == ir.SchemaKindDB {
		return append([]string{}, imp.Types...)
	}
	symbolSet := map[string]struct{}{}
	for _, symbol := range imp.Types {
		symbolSet[symbol] = struct{}{}
	}
	for symbol := range depSchema.Enums {
		symbolSet[symbol] = struct{}{}
	}
	for symbol := range depSchema.Unions {
		symbolSet[symbol] = struct{}{}
	}
	for symbol := range depSchema.Types {
		symbolSet[symbol] = struct{}{}
	}
	symbols := make([]string, 0, len(symbolSet))
	for symbol := range symbolSet {
		symbols = append(symbols, symbol)
	}
	return symbols
}
