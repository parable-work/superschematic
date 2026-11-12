package sdkgen

import (
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
	ir "github.com/parable-work/superschematic/ir"
)

// A filtered endpoint's filter in the TypeScript SDK (FilterBy, D106). The
// method takes it typed, as a member of its query object under the
// argument's name, and client.ts's withFilter writes it as the bracket
// form, filter[field]=value and filter[field][op]=value, beside the other
// query parameters. A paged method's <name>All hands the same query object
// to every page, so each page is asked with the same filter.

// FilterMember is one field of a filter's exported type: its key, the
// field's JSON name, and its type, a value, for equality, or an object of
// the field's operators.
type FilterMember struct {
	Key  string
	Type string
}

// filterTypeName is the exported type of the endpoint's filter: the
// namespace and the operation, as tools/index.ts names a tool's
// parameters, and Filter (CatalogListPartsFilter), so index.ts exports it
// beside another namespace's; "" for an endpoint without a filter.
func filterTypeName(ep apigen.EndpointInfo) string {
	if ep.Filter == nil {
		return ""
	}
	ns := ep.Namespace
	if ns == "" {
		ns = "root"
	}
	return tsutil.ToClassName(ns) + ep.Filter.TypeName
}

// filterValueTSType is the TypeScript type of one value of a filter's
// field, as a query parameter of its type has it in the tool parameters: a
// number for a whole or a fractional number, a boolean, the types
// package's enum, and a string for any other, a decimal, a date and a time
// among them, which the query carries as text.
func filterValueTSType(field apigen.FilterFieldInfo) string {
	switch field.Kind {
	case ir.FilterKindInt, ir.FilterKindFloat:
		return "number"
	case ir.FilterKindBool:
		return "boolean"
	case ir.FilterKindEnum:
		return normalizeTSTypeIdentifier(field.Value.Type)
	}
	return "string"
}

// filterMemberType is the type of a filter's member: a value of the
// field's type, or an object with a member per operator the field takes,
// in's a list of values, contains's text and isNull's a boolean.
func filterMemberType(field apigen.FilterFieldInfo) string {
	value := filterValueTSType(field)
	operators := make([]string, len(field.Operators))
	for i, op := range field.Operators {
		operand := value
		switch op {
		case ir.FilterOpIn:
			operand = value + "[]"
		case ir.FilterOpContains:
			operand = "string"
		case ir.FilterOpIsNull:
			operand = "boolean"
		}
		operators[i] = op + "?: " + operand
	}
	return value + " | { " + strings.Join(operators, "; ") + " }"
}

// FilterMembers are the members of the filter's exported type, a member
// per field a caller may filter by, in FilterBy's order.
func (e EndpointInfo) FilterMembers() []FilterMember {
	if e.Filter == nil {
		return nil
	}
	members := make([]FilterMember, len(e.Filter.Fields))
	for i, field := range e.Filter.Fields {
		members[i] = FilterMember{Key: tsPropertyName(field.Name), Type: filterMemberType(field)}
	}
	return members
}

// FilterParam is the member of the method's query object that holds the
// filter: the argument's name in camelCase, as a tool's parameters key it.
func (e EndpointInfo) FilterParam() string {
	if e.Filter == nil {
		return ""
	}
	return tsutil.ToCamelCase(e.Filter.Argument)
}

// QueryExpr is the query object a request sends, given the expression that
// holds the method's: the expression itself, or, for a filtered endpoint,
// withFilter of it, which writes the filter as the bracket form.
func (e EndpointInfo) QueryExpr(expr string) string {
	if e.Filter == nil {
		return expr
	}
	return "withFilter(" + expr + ", '" + e.FilterParam() + "')"
}

// QueryType is the type of the method's query object: any, or, for a
// filtered endpoint, an object whose filter member is the filter's type
// and whose other members are any.
func (e EndpointInfo) QueryType() string {
	if e.Filter == nil {
		return "any"
	}
	return "{ " + tsPropertyName(e.FilterParam()) + "?: " + e.FilterType + "; [name: string]: any }"
}

// filterTypeImports are the types of the types package the filter's type
// names: the enum of each enum field.
func (e EndpointInfo) filterTypeImports() []string {
	if e.Filter == nil {
		return nil
	}
	var names []string
	for _, field := range e.Filter.Fields {
		if field.Kind == ir.FilterKindEnum {
			names = append(names, normalizeTSTypeIdentifier(field.Value.Type))
		}
	}
	return names
}

// FilteredEndpoints are the namespace's endpoints that take a filter, its
// members' included, whose types its file declares.
func (n NamespaceInfo) FilteredEndpoints() []EndpointInfo {
	var out []EndpointInfo
	for _, endpoint := range n.AllEndpoints() {
		if endpoint.Filter != nil {
			out = append(out, endpoint)
		}
	}
	return out
}

// FilterMaxInValues is the most values a filter's in may list, which each
// filter's type documents.
func (NamespaceTemplateData) FilterMaxInValues() int {
	return ir.FilterMaxInValues
}

// FilterExample is a filter of the endpoint as the README writes one: a
// TypeScript object with the first field that is no enum held to a value
// and, when another takes one, a field under gte or contains. It is "" for
// a filter whose fields are all enums, whose values the README does not
// know.
func (e EndpointInfo) FilterExample() string {
	if e.Filter == nil {
		return ""
	}
	value := func(field apigen.FilterFieldInfo) string {
		switch field.Kind {
		case ir.FilterKindUUID:
			return "'0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40'"
		case ir.FilterKindInt:
			return "10"
		case ir.FilterKindFloat:
			return "2.5"
		case ir.FilterKindDecimal:
			return "'9.99'"
		case ir.FilterKindBool:
			return "true"
		case ir.FilterKindDateTime:
			return "'2026-01-01T00:00:00Z'"
		case ir.FilterKindDate:
			return "'2026-01-01'"
		}
		return "'anvil'"
	}
	var members []string
	first := -1
	for i, field := range e.Filter.Fields {
		if field.Kind != ir.FilterKindEnum {
			first = i
			members = append(members, tsPropertyName(field.Name)+": "+value(field))
			break
		}
	}
	if first < 0 {
		return ""
	}
	for _, op := range []string{ir.FilterOpGte, ir.FilterOpContains} {
		if len(members) > 1 {
			break
		}
		for i, field := range e.Filter.Fields {
			if i == first || field.Kind == ir.FilterKindEnum || !field.Takes(op) {
				continue
			}
			operand := value(field)
			if op == ir.FilterOpContains {
				operand = "'an'"
			}
			members = append(members, tsPropertyName(field.Name)+": { "+op+": "+operand+" }")
			break
		}
	}
	return "{ " + strings.Join(members, ", ") + " }"
}

// FilterMaxInValues and FilterMaxFilters are the most values a filter's
// in may list and the most filters a request may hold, which client.ts's
// filterParams refuses a filter beyond before the request.
func (*SDKOutput) FilterMaxInValues() int { return ir.FilterMaxInValues }

func (*SDKOutput) FilterMaxFilters() int { return ir.FilterMaxFilters }
