package apigen

import (
	"sort"

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

// OpenAPISortKey is the vendor-extension key, true, that marks a paged
// operation's sort argument (SortBy, D60 amended) on its parameter object,
// whose schema's enum holds its values. An OpenAPIHook renames it.
const OpenAPISortKey = "x-superschematic-sort"

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
	// Search is the options operation's search argument (SearchQuery,
	// D62), which a UI fills with the text a reader types to pick an item
	// (D61, amended); empty when it has none.
	Search string
	// Service is the API dependency whose operation the options are, by
	// its service name, when the API's own operations list no Target and
	// that dependency's do (D61, amended); empty for the API's own. The
	// options operation's names are then the dependency's, as its own
	// documents write them.
	Service string
}

// openAPIValue is the reference as the OpenAPI document writes it: the
// target as its component, and the options operation by its operationId,
// the dependency's when service names one.
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
	if r.Search != "" {
		value["search"] = r.Search
	}
	if r.Service != "" {
		value["service"] = r.Service
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
	if resolved.Search != nil {
		out.Search = resolved.Search.Name
	}
	out.Service = resolved.OptionsService
	return out
}

// endpointNames are the names the generated documents give an operation:
// its OpenAPI operationId, the handler name operationToEndpoint gives it
// (handlerName), and its tool name (ToolName), as the SDKs name it.
func endpointNames(set *ir.OperationSet, op *ir.FieldDef) (operationID, tool string) {
	namespace := extractNamespace(set.Name)
	return handlerName(namespace, op.Name), ToolName(namespace, op.Name)
}

// fieldReference is the reference field of holder holds, written or
// derived (ir.FieldReference), resolved in the API schema; nil when it
// holds none. A dependency's type is read against the API too: its
// RefersTo holds only when it resolves there to a value of the field's
// type, and its options are the API's own operation, or the dependency's
// when none of the API's lists it (D61, amended).
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
// those of the type it was built from (types, from addSchemaFromIR), the
// schema's own or a dependency's; a property the type does not declare is
// left alone.
func addOpenAPIFieldReferences(components map[string]interface{}, types map[string]*ir.TypeDef, schema *ir.Schema, dependencies map[string]*ir.Schema) {
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
		holder := types[name]
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
				openAPIExtensionTarget(property)[OpenAPIReferenceKey] = ref.openAPIValue()
			}
		}
	}
}

// openAPIExtensionTarget is the schema object a property's vendor keys go
// on: the property itself, unless it is a bare $ref, whose sibling keys
// OpenAPI 3.0.3 ignores (a Reference Object). Then the $ref moves into
// allOf, and the property, which keeps the keys beside it (a field's
// description), takes the vendor keys beside the allOf: an enum's or an
// object's component, referenced. A list's or a nullable value's property
// is no $ref and takes the keys as it is.
func openAPIExtensionTarget(property map[string]interface{}) map[string]interface{} {
	if ref, ok := property["$ref"]; ok {
		delete(property, "$ref")
		property["allOf"] = []interface{}{map[string]interface{}{"$ref": ref}}
	}
	return property
}

// openAPIParameterKeys gives a path or query parameter object, or a body
// argument's property schema, the reference, search and sort keys its
// argument holds: on a property that is a bare $ref, beside an allOf that
// holds it (openAPIExtensionTarget). A parameter object holds its schema
// under "schema", and takes them as it is.
func openAPIParameterKeys(target map[string]interface{}, param Param) {
	if param.Reference == nil && !param.Search && param.Sort == nil {
		return
	}
	target = openAPIExtensionTarget(target)
	if param.Reference != nil {
		target[OpenAPIReferenceKey] = param.Reference.openAPIValue()
	}
	if param.Search {
		target[OpenAPISearchKey] = true
	}
	if param.Sort != nil {
		target[OpenAPISortKey] = true
	}
}
