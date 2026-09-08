package pygen

import (
	"fmt"
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// resolvedImports holds the classified import surface of a schema for
// Python. Imported object types, enums, and unions are bound by name from
// the dependency package root (its __init__ re-exports everything), with an
// Any fallback when the dependency is not installed. Imported scalars are
// skipped: scalar aliases are regenerated locally from the IR's scalar
// definitions.
type resolvedImports struct {
	types   []ImportedTypeInfo
	imports []TypeImport

	// enumNames tracks imported enum names so default-literal classification
	// and model_rebuild detection recognise enum-typed fields whose enum
	// lives in a dependency.
	enumNames map[string]bool
}

// DependencyServiceName extracts the service name from a schema import
// package (e.g. "@parable-platform/web-db" -> "web-db").
func DependencyServiceName(pkg string) string {
	if idx := strings.LastIndex(pkg, "/"); idx >= 0 {
		return pkg[idx+1:]
	}
	return pkg
}

// resolveImports classifies every named import in the schema against its
// dependency schema.
func resolveImports(schema *ir.Schema, opts Options) (*resolvedImports, error) {
	result := &resolvedImports{enumNames: map[string]bool{}}
	if len(schema.Imports) == 0 {
		return result, nil
	}

	imports := append([]ir.Import{}, schema.Imports...)
	sort.Slice(imports, func(i, j int) bool {
		return imports[i].Package < imports[j].Package
	})

	moduleToTypeSet := map[string]map[string]struct{}{}
	localSymbols := localSchemaSymbols(schema)
	seenSymbols := map[string]struct{}{}

	for _, imp := range imports {
		depName := DependencyServiceName(imp.Package)
		depSchema, ok := opts.Dependencies[depName]
		if !ok || depSchema == nil {
			return nil, fmt.Errorf("pygen: dependency schema %q (package %q) not loaded", depName, imp.Package)
		}

		moduleName := opts.Naming.PythonTypesModule(moduleStem(depName))
		symbols := importSymbolsForDependency(imp, depSchema)
		sort.Strings(symbols)

		for _, symbol := range symbols {
			if _, ok := depSchema.Scalars[symbol]; ok {
				continue
			}
			// Locally defined types shadow dependency types of the same
			// name; emitting both would redeclare the symbol in models.
			if _, collides := localSymbols[symbol]; collides {
				continue
			}
			if _, seen := seenSymbols[symbol]; seen {
				continue
			}

			switch {
			case depSchema.Enums[symbol] != nil:
				result.enumNames[symbol] = true
			case depSchema.Unions[symbol] != nil:
				// Bound by name like enums; no extra metadata needed.
			case depSchema.Types[symbol] != nil:
				typeDef := depSchema.Types[symbol]
				// Traits are flattened into tables and are not emitted as
				// Python types in the dependency package.
				if typeDef.IsTrait || typeDef.Role == ir.RoleTrait {
					continue
				}
			default:
				return nil, fmt.Errorf("pygen: imported symbol %q not found in dependency schema %q", symbol, depName)
			}

			seenSymbols[symbol] = struct{}{}
			result.types = append(result.types, ImportedTypeInfo{
				Name:       symbol,
				ModuleName: moduleName,
			})
			if _, ok := moduleToTypeSet[moduleName]; !ok {
				moduleToTypeSet[moduleName] = map[string]struct{}{}
			}
			moduleToTypeSet[moduleName][symbol] = struct{}{}
		}
	}

	sort.Slice(result.types, func(i, j int) bool { return result.types[i].Name < result.types[j].Name })

	moduleNames := make([]string, 0, len(moduleToTypeSet))
	for moduleName := range moduleToTypeSet {
		moduleNames = append(moduleNames, moduleName)
	}
	sort.Strings(moduleNames)

	for _, moduleName := range moduleNames {
		typeSet := moduleToTypeSet[moduleName]
		typeNames := make([]string, 0, len(typeSet))
		for typeName := range typeSet {
			typeNames = append(typeNames, typeName)
		}
		sort.Strings(typeNames)
		result.imports = append(result.imports, TypeImport{
			ModuleName: moduleName,
			TypeNames:  typeNames,
		})
	}

	return result, nil
}

// importSymbolsForDependency returns symbols to bind from a dependency.
// DB schemas stay named-only (PARABLE-970); other kinds expand the catalog.
func importSymbolsForDependency(imp ir.Import, depSchema *ir.Schema) []string {
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

func localSchemaSymbols(schema *ir.Schema) map[string]struct{} {
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
