package apigen

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// FilterInfo is an endpoint's FilterBy (D106): the filters its list takes,
// as every writer reads them. The filter is no query parameter of one
// value, so it is not among the endpoint's QueryParams: a server reads it
// with its runtime's filter reader and hands the implementation a typed
// filter under Argument, and an SDK takes one and writes the bracket form.
type FilterInfo struct {
	// Argument is the argument's name, which an implementation takes the
	// filter under; GoName is its exported Go name.
	Argument string
	GoName   string
	// TypeName names the filter's type in PascalCase, <Operation>Filter
	// (ListIssuesFilter); a writer that needs it unique across namespaces
	// puts the namespace before it.
	TypeName string
	// Operation names the operation as the loader does, Set.operation, for
	// a generator's messages.
	Operation string
	// Fields are the fields a caller may filter by, in FilterBy's order.
	Fields []FilterFieldInfo
}

// FilterFieldInfo is one field of a FilterInfo.
type FilterFieldInfo struct {
	// Name is the item's field, by its JSON name; GoName its exported Go
	// name.
	Name   string
	GoName string
	// Kind is the field's kind, one of ir.FilterKinds, and Operators the
	// operators it takes, in ir.FilterOperatorOrder.
	Kind      string
	Operators []string
	// Value is one value of the field as a query argument named
	// filter[<field>]: its type, and the bodyargs.Arg the Go route decodes
	// it with, as an item of a query list of that type.
	Value BodyArg
}

// The shapes of a field's filter, which follow its operators: the typed
// filter of each runtime and SDK has one type per shape.
const (
	FilterShapeEquals  = "Equals"
	FilterShapeOrdered = "Ordered"
	FilterShapeText    = "Text"
)

// Takes reports whether the field takes the operator.
func (f FilterFieldInfo) Takes(op string) bool {
	return slices.Contains(f.Operators, op)
}

// Nullable reports whether the field may be null: it takes isNull.
func (f FilterFieldInfo) Nullable() bool {
	return f.Takes(ir.FilterOpIsNull)
}

// Shape is the shape of the field's filter: Text for one that takes
// contains, Ordered for one that takes gt, and Equals for any other.
func (f FilterFieldInfo) Shape() string {
	switch {
	case f.Takes(ir.FilterOpContains):
		return FilterShapeText
	case f.Takes(ir.FilterOpGt):
		return FilterShapeOrdered
	}
	return FilterShapeEquals
}

// ValueOperators are the field's operators that take a value of its type:
// every one but contains, whose value is text, and isNull, whose value is
// a boolean.
func (f FilterFieldInfo) ValueOperators() []string {
	var out []string
	for _, op := range f.Operators {
		if op != ir.FilterOpContains && op != ir.FilterOpIsNull {
			out = append(out, op)
		}
	}
	return out
}

// GoFilterType is the field's member of the Go implementation's filter:
// filterparse.Ordered[int64].
func (f FilterFieldInfo) GoFilterType() string {
	return "filterparse." + f.Shape() + "[" + f.Value.GoType + "]"
}

// GoDecode is the filterparse function that decodes the field's filters:
// filterparse.DecodeOrdered[int64].
func (f FilterFieldInfo) GoDecode() string {
	return "filterparse.Decode" + f.Shape() + "[" + f.Value.GoType + "]"
}

// GoSpec is the filter's filterparse.Spec as a Go literal, the route's
// reader's argument.
func (f *FilterInfo) GoSpec() string {
	fields := make([]string, len(f.Fields))
	for i, field := range f.Fields {
		operators := make([]string, len(field.Operators))
		for j, op := range field.Operators {
			operators[j] = strconv.Quote(op)
		}
		fields[i] = fmt.Sprintf("{Name: %s, Operators: []filterparse.Operator{%s}}", strconv.Quote(field.Name), strings.Join(operators, ", "))
	}
	return "filterparse.Spec{Fields: []filterparse.FieldSpec{" + strings.Join(fields, ", ") + "}}"
}

// FieldNames are the filter's fields, by their JSON names.
func (f *FilterInfo) FieldNames() []string {
	names := make([]string, len(f.Fields))
	for i, field := range f.Fields {
		names[i] = field.Name
	}
	return names
}

// FilterDescription describes an operation's filter in the OpenAPI
// document and the tool documents: the fields a caller may filter by.
func FilterDescription(filter *FilterInfo) string {
	return "The rows the list keeps: each member is a field (" + strings.Join(filter.FieldNames(), ", ") +
		") holding a value, for equality, or an object of operators, and all of them hold at once."
}

// FilterOperatorDescription describes one operator of a filter's field in
// the OpenAPI document and the tool documents.
func FilterOperatorDescription(op string) string {
	switch op {
	case ir.FilterOpEq:
		return "The field equals the value."
	case ir.FilterOpNe:
		return "The field is not the value: every item eq leaves out, one whose field is null among them."
	case ir.FilterOpIn:
		return fmt.Sprintf("The field is one of the values, at most %d.", ir.FilterMaxInValues)
	case ir.FilterOpNotIn:
		return fmt.Sprintf("The field is none of the values, at most %d: every item in leaves out, one whose field is null among them.", ir.FilterMaxInValues)
	case ir.FilterOpGt:
		return "The field is greater than the value."
	case ir.FilterOpGte:
		return "The field is the value or greater."
	case ir.FilterOpLt:
		return "The field is less than the value."
	case ir.FilterOpLte:
		return "The field is the value or less."
	case ir.FilterOpContains:
		return "The field's text holds the value, in any case."
	case ir.FilterOpIsNull:
		return "true keeps the items whose field is null, false the others."
	}
	return ""
}

// filterInfo is the endpoint's filter, nil for an operation without one.
// The loader fills each field's kind, operators and type (D106); IR built
// without it is refused here, since no writer classifies a field itself.
func (m *typeMapper) filterInfo(set *ir.OperationSet, op *ir.FieldDef) (*FilterInfo, error) {
	if op.Filter == nil {
		return nil, nil
	}
	owner := set.Name + "." + op.Name
	if op.Filter.Argument == "" || len(op.Filter.Fields) == 0 {
		return nil, fmt.Errorf("%s: its filter names no argument or no field", owner)
	}
	info := &FilterInfo{
		Argument:  op.Filter.Argument,
		GoName:    codegen.ToPascalCase(op.Filter.Argument),
		TypeName:  codegen.ToPascalCase(op.Name) + "Filter",
		Operation: owner,
	}
	for _, field := range op.Filter.Fields {
		if field == nil || field.Name == "" {
			return nil, fmt.Errorf("%s: its filter holds an unnamed field", owner)
		}
		if !slices.Contains(ir.FilterKinds, field.Kind) || field.TypeRef == nil || field.TypeRef.Name == "" ||
			!slices.Equal(field.Operators, ir.FilterOperators(field.Kind, slices.Contains(field.Operators, ir.FilterOpIsNull))) {
			return nil, fmt.Errorf("%s: filter field %s has no kind, type and operators of that kind; the loader fills them (D106)", owner, field.Name)
		}
		param, err := m.mapArgument(ir.FilterValueArgument(field))
		if err != nil {
			return nil, fmt.Errorf("%s: filter field %s: %w", owner, field.Name, err)
		}
		value := m.bodyArgs([]Param{param})[0]
		// Required marks a value the wire always carries; the Arg's rule is
		// for a body's absent member, which a filter's value never is.
		value.Options = slices.DeleteFunc(value.Options, func(option string) bool { return option == "bodyargs.Required()" })
		info.Fields = append(info.Fields, FilterFieldInfo{
			Name:      field.Name,
			GoName:    codegen.ToPascalCase(field.Name),
			Kind:      field.Kind,
			Operators: field.Operators,
			Value:     value,
		})
	}
	return info, nil
}

// GoFilterTypeName is the name of the Go type of the endpoint's filter in
// the API package: the namespace and the operation, as the handler is
// named, and Filter.
func (e EndpointInfo) GoFilterTypeName() string {
	return codegen.ToPascalCase(e.Namespace) + e.Filter.TypeName
}
