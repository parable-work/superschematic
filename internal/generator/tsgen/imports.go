package tsgen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// resolvedImports holds the classified import surface of a schema for
// TypeScript: type-only aliases for dependency-owned definitions, the
// package imports they require, and the package.json dependencies needed to
// resolve them. Imported scalars need no aliases; scalar symbols resolve
// through the scalar library (superscalar) every generated module depends on.
type resolvedImports struct {
	types               []ImportedTypeInfo
	imports             []TypeImport
	packageDependencies []PackageDependency

	// enumNames tracks imported enum names so default-literal classification
	// recognises enum-typed fields whose enum lives in a dependency.
	enumNames map[string]bool
}

// DependencyServiceName extracts the service name from a schema import
// package (e.g. "@schemas/web-db" -> "web-db").
func DependencyServiceName(pkg string) string {
	if idx := strings.LastIndex(pkg, "/"); idx >= 0 {
		return pkg[idx+1:]
	}
	return pkg
}

// resolveImports classifies every named import in the schema against its
// dependency schema. DB dependencies stay named-only so a single codegen
// import cannot publish the full secret-bearing catalog into the API barrel.
// General and other non-DB dependencies expand to the full
// catalog so consumers can keep importing enums/error codes from the API
// types package. Scalars are skipped because they resolve through
// superscalar. @source / foreign extends must not appear in schema.Imports --
// those are compile-time only in the TypeScript loader.
func resolveImports(schema *ir.Schema, opts Options) (*resolvedImports, error) {
	result := &resolvedImports{enumNames: map[string]bool{}}
	if len(schema.Imports) == 0 {
		return result, nil
	}

	usedAliases := map[string]struct{}{}
	aliasByDep := map[string]string{}
	packageByDep := map[string]string{}
	localSymbols := localSchemaSymbols(schema)
	seenSymbols := map[string]struct{}{}

	imports := append([]ir.Import{}, schema.Imports...)
	sort.Slice(imports, func(i, j int) bool {
		return imports[i].Package < imports[j].Package
	})

	for _, imp := range imports {
		depName := DependencyServiceName(imp.Package)
		depSchema, ok := opts.Dependencies[depName]
		if !ok || depSchema == nil {
			return nil, fmt.Errorf("tsgen: dependency schema %q (package %q) not loaded", depName, imp.Package)
		}

		pkgName := opts.DependencyPackages[depName]
		if pkgName == "" {
			pkgName = opts.Naming.NpmTypesPackage(depName)
		}

		alias, assigned := aliasByDep[depName]
		if !assigned {
			alias = codegen.UniqueImportAlias(codegen.SanitizeImportAlias(depName, nil), usedAliases)
			aliasByDep[depName] = alias
		}

		symbols := importSymbolsForDependency(imp, depSchema)
		sort.Strings(symbols)

		usedDep := false
		for _, symbol := range symbols {
			if _, ok := depSchema.Scalars[symbol]; ok {
				continue
			}
			// Locally defined types shadow dependency types of the same
			// name; emitting both would redeclare the symbol in types.ts.
			if _, collides := localSymbols[symbol]; collides {
				continue
			}
			if _, seen := seenSymbols[symbol]; seen {
				continue
			}
			seenSymbols[symbol] = struct{}{}

			var doc string
			isEnum := false
			isObject := false
			switch {
			case depSchema.Enums[symbol] != nil:
				enumDef := depSchema.Enums[symbol]
				doc = codegen.DocText(enumDef.Description, enumDef.Comment)
				result.enumNames[symbol] = true
				isEnum = true
			case depSchema.Unions[symbol] != nil:
				unionDef := depSchema.Unions[symbol]
				doc = codegen.DocText(unionDef.Description, unionDef.Comment)
			case depSchema.Types[symbol] != nil:
				typeDef := depSchema.Types[symbol]
				// Traits are flattened into tables and are not emitted as
				// TypeScript types in the dependency package.
				if typeDef.IsTrait || typeDef.Role == ir.RoleTrait {
					continue
				}
				doc = codegen.DocText(typeDef.Description, typeDef.Comment)
				isObject = true
			default:
				return nil, fmt.Errorf("tsgen: imported symbol %q not found in dependency schema %q", symbol, depName)
			}

			result.types = append(result.types, ImportedTypeInfo{
				Name:          symbol,
				Doc:           doc,
				ImportAlias:   alias,
				ImportPackage: pkgName,
				IsEnum:        isEnum,
				IsObject:      isObject,
			})
			usedDep = true
		}

		if usedDep {
			if _, ok := packageByDep[depName]; !ok {
				packageByDep[depName] = pkgName
				result.imports = append(result.imports, TypeImport{
					Alias:          alias,
					Path:           pkgName + "/types",
					DependencyName: depName,
				})
				result.packageDependencies = append(result.packageDependencies, PackageDependency{
					Name: pkgName,
					Spec: "file:../" + depName,
				})
			}
		}
	}

	sort.Slice(result.types, func(i, j int) bool { return result.types[i].Name < result.types[j].Name })
	sort.Slice(result.imports, func(i, j int) bool { return result.imports[i].Alias < result.imports[j].Alias })
	sort.Slice(result.packageDependencies, func(i, j int) bool {
		return result.packageDependencies[i].Name < result.packageDependencies[j].Name
	})

	return result, nil
}

// localSchemaSymbols returns the set of definition names owned by the schema
// itself; dependency symbols with the same name are shadowed and must not be
// re-exported.
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

// importSymbolsForDependency returns the symbols to alias from a dependency.
// DB schemas stay named-only; other kinds expand to the full catalog.
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
