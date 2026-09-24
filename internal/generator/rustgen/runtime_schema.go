package rustgen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"

	ir "github.com/parable-work/superschematic/ir"
)

// runtimeSchemas builds one IR document per @jsonField payload type: the
// type as root plus every definition reachable from it, imported ones
// included, rather than the whole service catalog. The crate ships them as
// schemas/<Type>.json, so a program that decodes the payload can read the
// contract the serde structs were generated from without mirroring it.
func runtimeSchemas(schema *ir.Schema, dependencies map[string]*ir.Schema) (map[string][]byte, error) {
	out := map[string][]byte{}
	for name, def := range schema.Types {
		if !def.JsonField {
			continue
		}
		doc := ir.NewSchema(schema.Name, ir.SchemaKindGeneral)
		doc.RootType = name
		seen := map[string]*ir.Schema{}
		var visit func(*ir.Schema, string) error
		visit = func(owner *ir.Schema, symbol string) error {
			resolved := runtimeSymbolOwner(owner, symbol, dependencies, map[string]bool{})
			if resolved == nil {
				switch symbol {
				case "string", "number", "boolean", "object", "any", "unknown", "void", "null":
					return nil
				default:
					return fmt.Errorf("rustgen: JSON payload %s references unresolved type %s", name, symbol)
				}
			}
			if previous := seen[symbol]; previous != nil {
				if previous != resolved {
					// Scalar definitions are copied into each service's IR, so
					// one scalar reached through two dependencies is the same
					// definition, not a conflict.
					if previous.Scalars[symbol] != nil && reflect.DeepEqual(previous.Scalars[symbol], resolved.Scalars[symbol]) {
						return nil
					}
					return fmt.Errorf("rustgen: JSON payload %s has ambiguous type %s", name, symbol)
				}
				return nil
			}
			seen[symbol] = resolved
			switch {
			case resolved.Types[symbol] != nil:
				typeDef := resolved.Types[symbol]
				doc.Types[symbol] = typeDef
				for _, field := range typeDef.Fields {
					if err := visit(resolved, field.TypeRef.Name); err != nil {
						return err
					}
				}
			case resolved.Enums[symbol] != nil:
				doc.Enums[symbol] = resolved.Enums[symbol]
			case resolved.Scalars[symbol] != nil:
				doc.Scalars[symbol] = resolved.Scalars[symbol]
			case resolved.Unions[symbol] != nil:
				union := resolved.Unions[symbol]
				doc.Unions[symbol] = union
				for _, member := range union.Types {
					if err := visit(resolved, member); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := visit(schema, name); err != nil {
			return nil, err
		}
		encoded, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("rustgen: encode JSON payload %s: %w", name, err)
		}
		out[name] = append(encoded, '\n')
	}
	return out, nil
}

// runtimeSymbolOwner returns the schema that defines symbol: this schema, or
// the dependency it imports symbol from, following imports transitively.
func runtimeSymbolOwner(schema *ir.Schema, symbol string, dependencies map[string]*ir.Schema, visiting map[string]bool) *ir.Schema {
	if schema.Types[symbol] != nil || schema.Enums[symbol] != nil || schema.Scalars[symbol] != nil || schema.Unions[symbol] != nil {
		return schema
	}
	if visiting[schema.Name] {
		return nil
	}
	visiting[schema.Name] = true
	imports := append([]ir.Import{}, schema.Imports...)
	sort.Slice(imports, func(i, j int) bool { return imports[i].Package < imports[j].Package })
	for _, imp := range imports {
		dep := dependencies[DependencyServiceName(imp.Package)]
		if dep != nil && slices.Contains(importSymbolsForDependency(imp, dep), symbol) {
			if owner := runtimeSymbolOwner(dep, symbol, dependencies, visiting); owner != nil {
				return owner
			}
		}
	}
	return nil
}

// writeRuntimeSchemas replaces <outputDir>/schemas with one <Type>.json per
// payload, and removes the directory when there are none, so a payload
// dropped from the schema leaves no stale file behind.
func writeRuntimeSchemas(schemas map[string][]byte, outputDir string) error {
	directory := filepath.Join(outputDir, "schemas")
	if err := os.RemoveAll(directory); err != nil {
		return fmt.Errorf("rustgen: clear generated runtime schemas: %w", err)
	}
	if len(schemas) == 0 {
		return nil
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	for name, data := range schemas {
		if err := os.WriteFile(filepath.Join(directory, name+".json"), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
