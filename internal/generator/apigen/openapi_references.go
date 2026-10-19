package apigen

import (
	"sort"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// OpenAPIReferenceKey is the vendor-extension key under which a value that
// refers to an item (D61) gives its resolved reference: on the property
// schema of a field or a body argument, and on the parameter object of a
// path or query argument. An OpenAPIHook renames it.
const OpenAPIReferenceKey = "x-superschematic-reference"

// OpenAPISearchKey is the vendor-extension key, true, that marks an
// operation's search argument (D62) on its parameter object. An
// OpenAPIHook renames it.
const OpenAPISearchKey = "x-superschematic-search"

// Reference is the reference a parameter or a field holds (D61, amended),
// resolved: written with RefersTo, or derived from a relation's key. The
// OpenAPI document and the tool documents write it under their reference
// keys, each naming the options operation its own way.
type Reference struct {
	// Target is the type the reference names.
	Target string
	// OptionsOperationID is the options operation's OpenAPI operationId,
	// its handler name ("CatalogListItemsHandler").
	OptionsOperationID string
	// OptionsTool is the options operation's tool name
	// ("catalog.listItems").
	OptionsTool string
	// Value is the target's JSON key the value holds.
	Value string
	// Label is the target's JSON key that names an item; empty when it has
	// none.
	Label string
	// Lookup is the options operation's argument that narrows its list to
	// the items with the values given; empty when it has none.
	Lookup string
}

// openAPIValue is the reference as the OpenAPI document writes it: the
// target as its component, and the options operation by its operationId.
func (r *Reference) openAPIValue() map[string]interface{} {
	value := map[string]interface{}{
		"target":  "#/components/schemas/" + r.Target,
		"options": r.OptionsOperationID,
		"value":   r.Value,
	}
	if r.Label != "" {
		value["label"] = r.Label
	}
	if r.Lookup != "" {
		value["lookup"] = r.Lookup
	}
	return value
}

// resolveReference resolves ref in the API schema, or returns nil when ref
// is nil or does not resolve: the loader refuses a written reference that
// does not resolve, and a derived one that does not is no reference.
func resolveReference(schema *ir.Schema, dependencies map[string]*ir.Schema, ref *ir.ReferenceDef) *Reference {
	if ref == nil {
		return nil
	}
	resolved, err := ir.ResolveReference(schema, dependencies, ref)
	if err != nil {
		return nil
	}
	operationID, tool := endpointNames(resolved.OptionsSet, resolved.Options)
	out := &Reference{
		Target:             resolved.TargetName,
		OptionsOperationID: operationID,
		OptionsTool:        tool,
		Value:              resolved.Value.Name,
	}
	if resolved.Label != nil {
		out.Label = resolved.Label.Name
	}
	if resolved.Lookup != nil {
		out.Lookup = resolved.Lookup.Name
	}
	return out
}

// endpointNames are the names the generated documents give an operation:
// its OpenAPI operationId, the handler name operationToEndpoint gives it,
// and its tool name, namespace.operation.
func endpointNames(set *ir.OperationSet, op *ir.FieldDef) (operationID, tool string) {
	namespace := extractNamespace(set.Name)
	return codegen.ToPascalCase(namespace) + codegen.ToPascalCase(op.Name) + "Handler", namespace + "." + op.Name
}

// fieldReference is the reference field of holder holds, written or
// derived (ir.FieldReference), resolved in the API schema; nil when it
// holds none. A dependency's type is read against the API too: the API's
// operations are the only ones its documents name, and its RefersTo holds
// only when it resolves there to a value of the field's type.
func fieldReference(schema *ir.Schema, dependencies map[string]*ir.Schema, holder *ir.TypeDef, field *ir.FieldDef) *Reference {
	ref, _ := ir.FieldReference(schema, dependencies, holder, field)
	return resolveReference(schema, dependencies, ref)
}

// fieldReference is the package's fieldReference in the mapper's schema.
func (m *typeMapper) fieldReference(holder *ir.TypeDef, field *ir.FieldDef) *Reference {
	return fieldReference(m.schema, m.dependencies, holder, field)
}

// addOpenAPIFieldReferences gives each property of the document's object
// components whose field holds a reference the reference key, on the
// property's own schema: on a list, the list's. A component's fields are
// the schema's own type of its name, else a dependency's, the
// dependencies in name order; a property the type does not declare is
// left alone.
func addOpenAPIFieldReferences(components map[string]interface{}, schema *ir.Schema, dependencies map[string]*ir.Schema) {
	names := make([]string, 0, len(components))
	for name := range components {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		component, ok := components[name].(map[string]interface{})
		if !ok {
			continue
		}
		properties, ok := component["properties"].(map[string]interface{})
		if !ok {
			continue
		}
		holder := componentType(schema, dependencies, name)
		if holder == nil {
			continue
		}
		for _, field := range holder.Fields {
			if field == nil {
				continue
			}
			property, ok := properties[field.Name].(map[string]interface{})
			if !ok {
				continue
			}
			if ref := fieldReference(schema, dependencies, holder, field); ref != nil {
				property[OpenAPIReferenceKey] = ref.openAPIValue()
			}
		}
	}
}

// componentType is the object type a component of the document is named
// after: the schema's own, else the first dependency's in name order.
func componentType(schema *ir.Schema, dependencies map[string]*ir.Schema, name string) *ir.TypeDef {
	if td := schema.Types[name]; td != nil {
		return td
	}
	for _, dep := range sortedSchemaNames(dependencies) {
		if dependencies[dep] == nil {
			continue
		}
		if td := dependencies[dep].Types[name]; td != nil {
			return td
		}
	}
	return nil
}

// openAPIParameterKeys gives a path or query parameter object, or a body
// argument's property schema, the reference and search keys its argument
// holds.
func openAPIParameterKeys(target map[string]interface{}, param Param) {
	if param.Reference != nil {
		target[OpenAPIReferenceKey] = param.Reference.openAPIValue()
	}
	if param.Search {
		target[OpenAPISearchKey] = true
	}
}
