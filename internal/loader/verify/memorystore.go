package verify

import (
	"fmt"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// checkMemoryStore checks a MemoryStore schema's declarations against the
// types they name (D75), in every form: each part is a required scalar
// whose wire form is a string, an integer or a boolean, or an enum, and
// the check records how it is written into a name (MemoryStorePart.Format);
// a payload is an object type the schema imports from a dependency, not
// @strictJSON, since a channel's decoder ignores the keys it does not know
// so that replicas of two releases share it. A payload's dependency is
// checked when the loader read it (Input.DependencySchemas). The kind's
// own Verify checks the rest (registry.verifyMemoryStore). A schema of
// another kind declares nothing of a memory store.
func checkMemoryStore(schema *ir.Schema, in Input, r *Result) {
	if schema.Kind != ir.SchemaKindMemoryStore {
		for _, decl := range schema.MemoryStoreDeclarations {
			if decl != nil {
				r.errorf(decl.File, "declaration %s (class %s): only a %s schema declares channels and keys, and %s is a %s service", decl.Name, decl.Symbol, ir.SchemaKindMemoryStore, schema.Name, schema.Kind)
			}
		}
		return
	}
	for _, decl := range schema.MemoryStoreDeclarations {
		if decl == nil || decl.Reserved {
			continue
		}
		where := fmt.Sprintf("declaration %s (class %s)", decl.Name, decl.Symbol)
		for i := range decl.Parts {
			part := &decl.Parts[i]
			format, err := memoryStorePartFormat(schema, in, part.Type)
			if err != nil {
				r.errorf(decl.File, "%s part %s: %v", where, part.Name, err)
				continue
			}
			if part.Format != "" && part.Format != format {
				r.errorf(decl.File, "%s part %s has format %s, but its type %s is written as %s", where, part.Name, part.Format, part.Type.Name, format)
				continue
			}
			part.Format = format
		}
		if decl.Payload != nil {
			if err := checkMemoryStorePayload(schema, in, decl.Payload.Name); err != nil {
				r.errorf(decl.File, "%s payload: %v", where, err)
			}
		}
	}
}

// memoryStorePartFormat is how a part of type ref is written into a name,
// or why the type is no part's: a string, a boolean, a scalar whose value
// is a string, an integer or a boolean, or an enum the schema imports.
func memoryStorePartFormat(schema *ir.Schema, in Input, ref ir.TypeRef) (ir.MemoryStorePartFormat, error) {
	if ref.IsArray || ref.IsMap {
		return "", fmt.Errorf("%s is a list or a map; a part is a required scalar", ref.Name)
	}
	switch ref.Name {
	case "string":
		return ir.MemoryStorePartString, nil
	case "boolean":
		return ir.MemoryStorePartBoolean, nil
	case "number":
		return "", fmt.Errorf("number is a floating-point number, whose text is not one value's; an integer part is Generic.Int64 or another scalar whose value is an integer")
	}
	if def := schema.Scalars[ref.Name]; def != nil {
		switch {
		case def.IsAnyJSON() || def.StructuredJSONType() != "" || def.LanguagePrimitive == ir.LanguageObject:
			return "", fmt.Errorf("scalar %s holds JSON, not one value; a part is a string, an integer, a boolean or an enum", ref.Name)
		case def.LanguagePrimitive == ir.LanguageBoolean:
			return ir.MemoryStorePartBoolean, nil
		case def.LanguagePrimitive == ir.LanguageNumber && def.TypeMappings["json_schema"] == "integer":
			return ir.MemoryStorePartInteger, nil
		case def.LanguagePrimitive == ir.LanguageNumber:
			return "", fmt.Errorf("scalar %s is a floating-point number, whose text is not one value's; a part is a string, an integer, a boolean or an enum", ref.Name)
		case def.LanguagePrimitive == ir.LanguageString:
			return ir.MemoryStorePartString, nil
		}
		return "", fmt.Errorf("scalar %s has primitive %q; a part is a string, an integer, a boolean or an enum", ref.Name, def.LanguagePrimitive)
	}
	if in.ExternalEnums[ref.Name] != nil || schema.Enums[ref.Name] != nil {
		return ir.MemoryStorePartEnum, nil
	}
	if dep := importedFrom(schema, in, ref.Name); dep != nil {
		switch {
		case dep.Enums[ref.Name] != nil:
			return ir.MemoryStorePartEnum, nil
		case dep.Scalars[ref.Name] != nil:
			return memoryStorePartFormat(dep, Input{}, ref)
		case dep.Types[ref.Name] != nil || dep.Unions[ref.Name] != nil:
			return "", fmt.Errorf("%s is an object type of %s; a part is a string, an integer, a boolean or an enum", ref.Name, dep.Name)
		}
	}
	if serviceOf(schema, ref.Name) != "" && in.DependencySchemas == nil {
		// A data form read without its dependencies: the name is an
		// import, and the build's load checks it.
		return ir.MemoryStorePartEnum, nil
	}
	return "", fmt.Errorf("%s is not a scalar, and no enum the schema imports; a part is a string, an integer, a boolean or an enum", ref.Name)
}

// checkMemoryStorePayload checks a channel's payload: an object type the
// schema imports from a dependency, which, when the loader read the
// dependency, it declares and does not mark @strictJSON.
func checkMemoryStorePayload(schema *ir.Schema, in Input, name string) error {
	if schema.Enums[name] != nil || schema.Scalars[name] != nil || in.ExternalEnums[name] != nil {
		return fmt.Errorf("%s is not an object type; a message is one object of a type a dependency declares", name)
	}
	service := serviceOf(schema, name)
	if service == "" {
		return fmt.Errorf("%s is imported from no dependency; a channel's payload is an object type of a dependency of the store's schema, whose types package each language generates", name)
	}
	if kind, ok := in.Dependencies[service]; in.Dependencies != nil && !ok {
		return fmt.Errorf("%s is imported from %s, which the config does not list among its dependencies", name, service)
	} else if ok && kind != ir.SchemaKindGeneral && kind != ir.SchemaKindAPI && kind != ir.SchemaKindDB {
		return fmt.Errorf("%s is imported from %s, a %s service; a payload is a type of a General, API or DB service", name, service, kind)
	}
	dep := in.DependencySchemas[service]
	if dep == nil {
		return nil
	}
	td := dep.Types[name]
	switch {
	case td == nil && (dep.Enums[name] != nil || dep.Scalars[name] != nil || dep.Unions[name] != nil):
		return fmt.Errorf("%s of %s is not an object type; a message is one object", name, service)
	case td == nil:
		return fmt.Errorf("%s declares no type %s", service, name)
	case td.StrictJSON:
		return fmt.Errorf("%s of %s is @strictJSON, whose decoder refuses a key it does not know; a channel's decoder ignores them, so that replicas of two releases share the channel while a deploy rolls", name, service)
	}
	return nil
}

// serviceOf is the service the schema imports symbol from, or "".
func serviceOf(schema *ir.Schema, symbol string) string {
	for _, imp := range schema.Imports {
		if slices.Contains(imp.Types, symbol) {
			pkg := imp.Package
			if i := strings.LastIndex(pkg, "/"); i >= 0 {
				pkg = pkg[i+1:]
			}
			return pkg
		}
	}
	return ""
}

// importedFrom is the loaded schema of the dependency the schema imports
// symbol from, or nil when the loader read none.
func importedFrom(schema *ir.Schema, in Input, symbol string) *ir.Schema {
	if service := serviceOf(schema, symbol); service != "" {
		return in.DependencySchemas[service]
	}
	return nil
}
