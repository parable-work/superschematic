package typegen

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// resolvedImports holds the classified import surface of a schema: aliases
// for dependency-owned enums, types, and unions, plus the Go imports they
// require. Imported scalars need no aliases; scalar symbols resolve through
// the shared scalar-lib import that every generated module already carries.
type resolvedImports struct {
	enums   []ImportedEnumInfo
	types   []ImportedTypeInfo
	unions  []ImportedUnionInfo
	imports []ModuleImport

	// modulePaths maps dependency service name to its generated Go module
	// path, for go.mod require/replace emission.
	modulePaths map[string]string
}

// moduleDependencies returns the sorted Go module paths of all dependency
// modules actually used by aliases, excluding the module itself.
func (r *resolvedImports) moduleDependencies(selfModulePath string) []string {
	var deps []string
	for _, modPath := range r.modulePaths {
		if modPath == selfModulePath {
			continue
		}
		deps = append(deps, modPath)
	}
	sort.Strings(deps)
	return deps
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
// dependency schema and produces alias metadata grouped by definition kind.
// DB dependencies stay named-only (PARABLE-970); other kinds expand to the
// full catalog. Imports are always named in v2 (wildcards do not exist).
func resolveImports(schema *ir.Schema, opts Options) (*resolvedImports, error) {
	result := &resolvedImports{modulePaths: map[string]string{}}
	if len(schema.Imports) == 0 {
		return result, nil
	}

	usedAliases := map[string]struct{}{}
	aliasByDep := map[string]string{}
	localSymbols := localSchemaSymbols(schema)
	seenEnums := map[string]struct{}{}
	seenTypes := map[string]struct{}{}
	seenUnions := map[string]struct{}{}

	// Sort imports by package for deterministic alias assignment.
	imports := append([]ir.Import{}, schema.Imports...)
	sort.Slice(imports, func(i, j int) bool {
		return imports[i].Package < imports[j].Package
	})

	for _, imp := range imports {
		depName := DependencyServiceName(imp.Package)
		depSchema, ok := opts.Dependencies[depName]
		if !ok || depSchema == nil {
			return nil, fmt.Errorf("typegen: dependency schema %q (package %q) not loaded", depName, imp.Package)
		}

		modulePath := opts.DependencyModules[depName]
		if modulePath == "" {
			modulePath = deriveDependencyModulePath(opts.ModulePath, depName)
		}

		alias, assigned := aliasByDep[depName]
		if !assigned {
			alias = codegen.UniqueImportAlias(codegen.SanitizeImportAlias(depName, nil), usedAliases)
			aliasByDep[depName] = alias
		}

		depEnums := codegen.ExtractEnums(depSchema)
		enumsByName := make(map[string]codegen.EnumInfo, len(depEnums))
		for _, e := range depEnums {
			enumsByName[e.Name] = e
		}

		// DB deps stay named-only (PARABLE-970); other kinds expand the
		// full catalog so API packages keep re-exporting shared enums.
		symbolSet := map[string]struct{}{}
		for _, symbol := range imp.Types {
			symbolSet[symbol] = struct{}{}
		}
		if depSchema.Kind != ir.SchemaKindDB {
			for symbol := range depSchema.Enums {
				symbolSet[symbol] = struct{}{}
			}
			for symbol := range depSchema.Unions {
				symbolSet[symbol] = struct{}{}
			}
			for symbol := range depSchema.Types {
				symbolSet[symbol] = struct{}{}
			}
		}
		symbols := make([]string, 0, len(symbolSet))
		for symbol := range symbolSet {
			symbols = append(symbols, symbol)
		}
		sort.Strings(symbols)

		usedModule := false
		for _, symbol := range symbols {
			if _, collides := localSymbols[symbol]; collides {
				continue
			}

			if enumInfo, ok := enumsByName[symbol]; ok {
				if _, seen := seenEnums[symbol]; seen {
					continue
				}
				seenEnums[symbol] = struct{}{}
				result.enums = append(result.enums, ImportedEnumInfo{
					Name:        symbol,
					Doc:         enumInfo.Doc(),
					ImportAlias: alias,
					Values:      enumInfo.Values,
				})
				usedModule = true
				continue
			}

			if unionDef, ok := depSchema.Unions[symbol]; ok {
				if _, seen := seenUnions[symbol]; seen {
					continue
				}
				seenUnions[symbol] = struct{}{}
				result.unions = append(result.unions, ImportedUnionInfo{
					Name:        symbol,
					Doc:         codegen.DocText(unionDef.Description, unionDef.Comment),
					ImportAlias: alias,
				})
				usedModule = true
				continue
			}

			if _, ok := depSchema.Scalars[symbol]; ok {
				// Scalars resolve through scalar-lib; no per-module alias.
				continue
			}

			if typeDef, ok := depSchema.Types[symbol]; ok {
				if addImportedTypeAliases(&result.types, seenTypes, localSymbols, depSchema, typeDef, alias) {
					usedModule = true
				}
				continue
			}

			return nil, fmt.Errorf("typegen: imported symbol %q not found in dependency schema %q", symbol, depName)
		}

		if usedModule {
			result.modulePaths[depName] = modulePath
			exists := false
			for _, mi := range result.imports {
				if mi.Alias == alias {
					exists = true
					break
				}
			}
			if !exists {
				result.imports = append(result.imports, ModuleImport{Alias: alias, Path: modulePath})
			}
		}
	}

	sort.Slice(result.enums, func(i, j int) bool { return result.enums[i].Name < result.enums[j].Name })
	sort.Slice(result.types, func(i, j int) bool { return result.types[i].Name < result.types[j].Name })
	sort.Slice(result.unions, func(i, j int) bool { return result.unions[i].Name < result.unions[j].Name })
	sort.Slice(result.imports, func(i, j int) bool { return result.imports[i].Alias < result.imports[j].Alias })

	return result, nil
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

// addImportedTypeAliases records a dependency type alias and walks nested
// object field types. Returns true when at least one alias was emitted.
//
// Locally defined symbols always win: @source(DBType) must not redeclare an
// API view that shares a name with a nested DB relation (e.g. web-api.Tenant
// vs web-db.Tenant). Traits are skipped -- they are flattened into tables and
// are not emitted as Go types in the dependency module.
func addImportedTypeAliases(result *[]ImportedTypeInfo, seen map[string]struct{}, localSymbols map[string]struct{}, depSchema *ir.Schema, typeDef *ir.TypeDef, alias string) bool {
	if typeDef == nil {
		return false
	}
	if typeDef.IsTrait || typeDef.Role == ir.RoleTrait {
		return false
	}
	if _, ok := localSymbols[typeDef.Name]; ok {
		return false
	}
	if _, ok := seen[typeDef.Name]; ok {
		return false
	}
	seen[typeDef.Name] = struct{}{}
	*result = append(*result, ImportedTypeInfo{
		Name:        typeDef.Name,
		Doc:         codegen.DocText(typeDef.Description, typeDef.Comment),
		ImportAlias: alias,
		IsInput:     typeDef.Role == ir.RoleAPIInput,
	})
	emitted := true
	for _, field := range typeDef.Fields {
		nested, ok := depSchema.Types[field.TypeRef.Name]
		if !ok {
			continue
		}
		if addImportedTypeAliases(result, seen, localSymbols, depSchema, nested, alias) {
			emitted = true
		}
	}
	return emitted
}

// deriveDependencyModulePath derives a sibling module path from the current
// module path by swapping the last path segment for the dependency name.
// Generated type modules are laid out as siblings under types/go/.
func deriveDependencyModulePath(selfModulePath, depName string) string {
	if selfModulePath == "" {
		return depName
	}
	return path.Join(path.Dir(selfModulePath), depName)
}
