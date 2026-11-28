package tsgen

import (
	"fmt"
	"slices"
	"sort"

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

	// enumDefs are the imported enums by name, whose members a state
	// machine on a field of one names its states by (D118).
	enumDefs map[string]*ir.EnumDef
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
	result := &resolvedImports{enumNames: map[string]bool{}, enumDefs: map[string]*ir.EnumDef{}}
	if len(schema.Imports) == 0 {
		return result, nil
	}

	usedAliases := map[string]struct{}{}
	aliasByDep := map[string]string{}
	packageByDep := map[string]string{}
	localSymbols := codegen.LocalSchemaSymbols(schema)
	seenSymbols := map[string]struct{}{}

	imports := append([]ir.Import{}, schema.Imports...)
	sort.Slice(imports, func(i, j int) bool {
		return imports[i].Package < imports[j].Package
	})

	for _, imp := range imports {
		depName := codegen.DependencyServiceName(imp.Package)
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

		symbols := codegen.ImportSymbols(imp, depSchema)
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
			isUnion := false
			hasValidators := false
			switch {
			case depSchema.Enums[symbol] != nil:
				enumDef := depSchema.Enums[symbol]
				doc = codegen.DocText(enumDef.Description, enumDef.Comment)
				result.enumNames[symbol] = true
				result.enumDefs[symbol] = enumDef
				isEnum = true
			case depSchema.Unions[symbol] != nil:
				unionDef := depSchema.Unions[symbol]
				doc = codegen.DocText(unionDef.Description, unionDef.Comment)
				isUnion = true
			case depSchema.Types[symbol] != nil:
				typeDef := depSchema.Types[symbol]
				// Traits are flattened into tables and are not emitted as
				// TypeScript types in the dependency package.
				if typeDef.IsTrait || typeDef.Role == ir.RoleTrait {
					continue
				}
				doc = codegen.DocText(typeDef.Description, typeDef.Comment)
				isObject = true
				hasValidators = slices.Contains(localObjectRoles, typeDef.Role) || typeDef.Role == ir.RoleAPIInput
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
				IsUnion:       isUnion,
				HasValidators: hasValidators,
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
				// The dependency is a member of the output root's Bun
				// workspace, so it is named by workspace:* (see WorkspaceRoot).
				result.packageDependencies = append(result.packageDependencies, PackageDependency{
					Name: pkgName,
					Spec: "workspace:*",
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
