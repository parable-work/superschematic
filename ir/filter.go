package ir

import "slices"

// A list's filters (D106). An operation takes them as one argument,
// FilterBy<Item, Fields> from @superschematic/api:
//
//	listIssues(filter: FilterBy<IssueView, ["assigneeId", "priority"]>): IssueView[]
//
// The reader records the argument as FieldDef.Filter, its name and its
// fields, and leaves it out of FieldDef.Arguments: it is no query
// parameter of one value. The loader holds each field to the operation's
// item and fills its kind, its type and its operators, so a generator
// never classifies a field itself. On the wire a filter is the bracket
// form, filter[field]=value for equality and filter[field][op]=value for
// an operator, whatever the argument is called; members are ANDed.

// FilterParam is the query parameter every filter travels under:
// filter[field] and filter[field][op].
const FilterParam = "filter"

// The limits of a request's filters (D106).
const (
	// FilterMaxFilters is the most filter parameters a request may hold.
	FilterMaxFilters = 50
	// FilterMaxInValues is the most values an in may list.
	FilterMaxInValues = 100
)

// The kinds of a filter's field, which decide its operators
// ([FilterOperators]) and how a server reads its values.
const (
	FilterKindUUID     = "uuid"
	FilterKindString   = "string"
	FilterKindEnum     = "enum"
	FilterKindInt      = "int"
	FilterKindFloat    = "float"
	FilterKindDecimal  = "decimal"
	FilterKindBool     = "bool"
	FilterKindDateTime = "datetime"
	FilterKindDate     = "date"
)

// FilterKinds are the kinds of a filter's field, in the order the
// documentation lists them.
var FilterKinds = []string{
	FilterKindUUID, FilterKindString, FilterKindEnum, FilterKindInt, FilterKindFloat,
	FilterKindDecimal, FilterKindBool, FilterKindDateTime, FilterKindDate,
}

// The operators of a filter, as the wire names them.
const (
	FilterOpEq       = "eq"
	FilterOpIn       = "in"
	FilterOpGt       = "gt"
	FilterOpGte      = "gte"
	FilterOpLt       = "lt"
	FilterOpLte      = "lte"
	FilterOpContains = "contains"
	FilterOpIsNull   = "isNull"
)

// FilterOperatorOrder is every operator, in the order a field lists its
// own and a reader answers them.
var FilterOperatorOrder = []string{
	FilterOpEq, FilterOpIn, FilterOpGt, FilterOpGte, FilterOpLt, FilterOpLte, FilterOpContains, FilterOpIsNull,
}

// FilterOperators are the operators of a field of kind (D106): eq and in
// on every kind; gt, gte, lt and lte on a number, a date and a time;
// contains on text; and isNull on a field that may be null. Nil for a kind
// that is none of [FilterKinds].
func FilterOperators(kind string, nullable bool) []string {
	var ops []string
	switch kind {
	case FilterKindUUID, FilterKindEnum, FilterKindBool:
		ops = []string{FilterOpEq, FilterOpIn}
	case FilterKindString:
		ops = []string{FilterOpEq, FilterOpIn, FilterOpContains}
	case FilterKindInt, FilterKindFloat, FilterKindDecimal, FilterKindDateTime, FilterKindDate:
		ops = []string{FilterOpEq, FilterOpIn, FilterOpGt, FilterOpGte, FilterOpLt, FilterOpLte}
	default:
		return nil
	}
	if nullable {
		ops = append(ops, FilterOpIsNull)
	}
	return ops
}

// OperationFilter is FilterBy (D106): the filters a list takes.
type OperationFilter struct {
	// Argument is the argument's name, which an implementation takes the
	// filter under. The wire's parameter is [FilterParam] whatever it is.
	Argument string `json:"argument" yaml:"argument"`

	// Fields are the fields a caller may filter by, each a field of the
	// operation's item, in the order FilterBy names them.
	Fields []*FilterField `json:"fields" yaml:"fields"`
}

// FilterField is one field of an [OperationFilter].
type FilterField struct {
	// Name is the item's field, by its JSON name.
	Name string `json:"name" yaml:"name"`

	// Kind is the field's kind, one of [FilterKinds]. The loader fills it
	// from the item's field; a data form may leave it out, and one that
	// writes another is refused.
	Kind string `json:"kind,omitempty" yaml:"kind,omitempty"`

	// Operators are the operators the field takes ([FilterOperators]), in
	// [FilterOperatorOrder]. The loader fills them as it fills Kind.
	Operators []string `json:"operators,omitempty" yaml:"operators,omitempty"`

	// TypeRef is the type of one value of the field, the item's field's,
	// which the loader fills as it fills Kind.
	TypeRef *TypeRef `json:"typeRef,omitempty" yaml:"typeRef,omitempty"`
}

// Takes reports whether the field takes the operator.
func (f *FilterField) Takes(op string) bool {
	return f != nil && slices.Contains(f.Operators, op)
}

// Nullable reports whether the field may be null: it takes isNull.
func (f *FilterField) Nullable() bool {
	return f.Takes(FilterOpIsNull)
}

// Field finds a filter's field by its name, or nil.
func (o *OperationFilter) Field(name string) *FilterField {
	if o == nil {
		return nil
	}
	for _, f := range o.Fields {
		if f != nil && f.Name == name {
			return f
		}
	}
	return nil
}

// FilterValueArgument is the argument one value of a filter's field is
// read as: a query parameter of the field's type under filter[field], so a
// generator reads a filter's value exactly as it reads an item of a query
// list of that type.
// The field's TypeRef is set: the loader's.
func FilterValueArgument(field *FilterField) *ArgumentDef {
	return &ArgumentDef{
		Name:     FilterParam + "[" + field.Name + "]",
		TypeRef:  *field.TypeRef,
		Required: true,
		IsQuery:  true,
	}
}
