package rustgen

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	ir "github.com/parable-work/superschematic/ir"
)

// resolvedImports holds the classified import surface of a schema for Rust:
// `pub type` aliases for dependency-owned definitions, the crate imports
// they require, and the Cargo.toml path dependencies needed to resolve
// them. Imported scalars need no aliases; scalar aliases are regenerated
// locally from the IR's scalar definitions.
type resolvedImports struct {
	types             []ImportedTypeInfo
	imports           []TypeImport
	cargoDependencies []CargoDependency

	// enumNames tracks imported enum names so default-literal classification
	// recognises enum-typed fields whose enum lives in a dependency.
	enumNames map[string]bool

	// enums carries the imported enums' values so @default literals on
	// fields typed by a dependency enum (e.g. a union discriminator) can be
	// rendered as Rust variant expressions.
	enums []codegen.EnumInfo
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
// dependency schema. DB dependencies stay named-only (PARABLE-970); other
// kinds expand to the full catalog. Imported definitions become `pub type`
// aliases re-exported from types.rs; scalars are skipped because their
// aliases are regenerated locally.
func resolveImports(schema *ir.Schema, opts Options) (*resolvedImports, error) {
	result := &resolvedImports{enumNames: map[string]bool{}}
	if len(schema.Imports) == 0 {
		return result, nil
	}

	usedAliases := map[string]struct{}{}
	aliasByDep := map[string]string{}
	crateByDep := map[string]string{}
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
			return nil, fmt.Errorf("rustgen: dependency schema %q (package %q) not loaded", depName, imp.Package)
		}

		crateName := opts.DependencyCrates[depName]
		if crateName == "" {
			crateName = opts.Naming.RustTypesCrate(depName)
		}

		alias, assigned := aliasByDep[depName]
		if !assigned {
			alias = codegen.UniqueImportAlias(codegen.SanitizeImportAlias(depName, rustutil.IsRustKeyword), usedAliases)
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
			// name; emitting both would redeclare the symbol in types.rs.
			if _, collides := localSymbols[symbol]; collides {
				continue
			}
			if _, seen := seenSymbols[symbol]; seen {
				continue
			}

			var doc string
			switch {
			case depSchema.Enums[symbol] != nil:
				enumDef := depSchema.Enums[symbol]
				doc = codegen.DocText(enumDef.Description, enumDef.Comment)
				result.enumNames[symbol] = true
				result.enums = append(result.enums, importedEnumInfo(enumDef))
			case depSchema.Unions[symbol] != nil:
				unionDef := depSchema.Unions[symbol]
				doc = codegen.DocText(unionDef.Description, unionDef.Comment)
			case depSchema.Types[symbol] != nil:
				typeDef := depSchema.Types[symbol]
				// Traits are flattened into tables and are not emitted as
				// Rust types in the dependency crate.
				if typeDef.IsTrait || typeDef.Role == ir.RoleTrait {
					continue
				}
				doc = codegen.DocText(typeDef.Description, typeDef.Comment)
			default:
				return nil, fmt.Errorf("rustgen: imported symbol %q not found in dependency schema %q", symbol, depName)
			}

			seenSymbols[symbol] = struct{}{}
			result.types = append(result.types, ImportedTypeInfo{
				Name:        symbol,
				Doc:         doc,
				ImportAlias: alias,
			})
			usedDep = true
		}

		if usedDep {
			if _, ok := crateByDep[depName]; !ok {
				crateByDep[depName] = crateName
				result.imports = append(result.imports, TypeImport{
					Alias:          alias,
					CrateName:      crateName,
					CrateModule:    rustutil.CrateNameToModulePath(crateName),
					DependencyName: depName,
				})
				result.cargoDependencies = append(result.cargoDependencies, CargoDependency{
					Name:         crateName,
					RelativePath: filepath.ToSlash(filepath.Join("..", depName)),
				})
			}
		}
	}

	sort.Slice(result.enums, func(i, j int) bool { return result.enums[i].Name < result.enums[j].Name })
	sort.Slice(result.types, func(i, j int) bool { return result.types[i].Name < result.types[j].Name })
	sort.Slice(result.imports, func(i, j int) bool { return result.imports[i].Alias < result.imports[j].Alias })
	sort.Slice(result.cargoDependencies, func(i, j int) bool {
		return result.cargoDependencies[i].Name < result.cargoDependencies[j].Name
	})

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

// importSymbolsForDependency returns symbols to alias from a dependency.
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

// importedEnumInfo converts a dependency-owned ir.EnumDef into the codegen
// enum shape, mirroring codegen.ExtractEnums' value handling (SerializedAs
// wins over the declared name).
func importedEnumInfo(enumDef *ir.EnumDef) codegen.EnumInfo {
	info := codegen.EnumInfo{
		Name:        enumDef.Name,
		Owner:       enumDef.Owner,
		Description: enumDef.Description,
		Comment:     enumDef.Comment,
		Values:      make([]codegen.EnumValueInfo, 0, len(enumDef.Values)),
	}
	for _, val := range enumDef.Values {
		valueInfo := codegen.EnumValueInfo{
			Name:        val.Name,
			Value:       val.Name,
			Description: val.Description,
			Comment:     val.Comment,
		}
		if val.SerializedAs != "" {
			valueInfo.Value = val.SerializedAs
		}
		info.Values = append(info.Values, valueInfo)
	}
	return info
}
