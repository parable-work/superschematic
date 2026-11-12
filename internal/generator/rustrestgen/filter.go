package rustrestgen

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	ir "github.com/parable-work/superschematic/ir"
)

// FilterInfo is an operation's FilterBy (D106) as the crate renders it.
// Its Args struct has the associated const FILTER, the runtime's
// FilterSpec of the fields a caller may filter by, by which the router
// reads the request's filters from the bracket form's query parameters,
// filter[field]=value and filter[field][op]=value; the Args carry them
// typed in Field, a TypeName whose members are the fields, each the
// runtime's filter::Equals, Ordered or Text of the field's type, which the
// router decodes each value into as an item of a query list of that type.
// The Args' check holds a filter a caller built to the same rules. The
// filter is not one of the Args' query fields.
type FilterInfo struct {
	// Argument is the argument's name in the schema, and Field the Args
	// struct's field that holds the filter.
	Argument string
	Field    string
	// TypeName is the filter's struct in the interfaces: the operation's
	// handler name and Filter, CatalogListPartsFilter.
	TypeName string
	// Operation names the operation as the loader does, Set.operation.
	Operation string
	// Fields are the fields a caller may filter by, in FilterBy's order.
	Fields []FilterFieldInfo
}

// FilterFieldInfo is one field of a FilterInfo: a member of the filter's
// struct.
type FilterFieldInfo struct {
	// Name is the item's field, by its JSON name, and Field the member of
	// the filter's struct.
	Name  string
	Field string
	// Kind is the field's kind, one of ir.FilterKinds.
	Kind string
	// Shape is the runtime's type of the field's filter, which follows its
	// operators: Equals, Ordered or Text (apigen.FilterFieldInfo.Shape).
	Shape string
	// Operators are the operators the field takes, as the wire names them.
	Operators []string
	// Value is one value of the field as a query parameter named
	// filter[<field>]: its RustType the type of the filter's members, and
	// its Spec the ParamSpec static the router decodes each value with, as
	// an item of a query list of that type. A date's and a decimal's value,
	// which a query parameter takes as text, is read by the scalar core's
	// parse instead (ParsedScalar), as its canonical text.
	Value ParamInfo
}

// ParsedScalar is a scalar whose parse reads a filter's value (D106): the
// value's ParamKind, the scalar's canonical name, and the router's
// function that hands the text to the scalar crate's registry.
type ParsedScalar struct {
	Kind      string // Date
	Canonical string // Temporal.Date
	Fn        string // parse_temporal_date
}

// ScalarParseInfo is what the router and the manifest need to read a
// filter's dates and decimals by the scalar core (D106): the scalar
// crate, which the API crate then depends on, and its registry.
type ScalarParseInfo struct {
	// Crate is the scalar crate's Cargo name and Registry the Rust
	// expression of its registry (Naming.ScalarRustRegistryExpr).
	Crate    string
	Registry string
	// DepPath is the scalar crate's directory relative to the crate's, set
	// by SetReplacePaths; empty names the crate's version.
	DepPath string
	// Scalars are the scalars the router parses, each a function of it.
	Scalars []ParsedScalar
}

// parsedKinds are the kinds of a filter's field whose value the scalar
// core parses, with the scalar each is read as: the two a query parameter
// takes as text held only to its scalar's IR rules.
var parsedKinds = map[string]ParsedScalar{
	ir.FilterKindDate:    {Kind: "Date", Canonical: "Temporal.Date"},
	ir.FilterKindDecimal: {Kind: "Decimal", Canonical: "Generic.Decimal"},
}

// parsedScalar is the scalar the core parses a field of kind's value as,
// or nil for a kind a query parameter's own rules read. The router gets a
// function for each one named.
func (b *builder) parsedScalar(kind string) *ParsedScalar {
	parsed, ok := parsedKinds[kind]
	if !ok {
		return nil
	}
	for i := range b.parses {
		if b.parses[i].Canonical == parsed.Canonical {
			return &b.parses[i]
		}
	}
	parsed.Fn = "parse_" + codegen.ToSnakeCase(scalarSymbol(parsed.Canonical))
	b.parses = append(b.parses, parsed)
	return &b.parses[len(b.parses)-1]
}

// scalarParse is what the crate needs to parse the scalars its filters
// read, nil when none does.
func (b *builder) scalarParse() *ScalarParseInfo {
	if len(b.parses) == 0 {
		return nil
	}
	return &ScalarParseInfo{
		Crate:    b.naming.ScalarRustCrate,
		Registry: b.naming.ScalarRustRegistryExpr(),
		Scalars:  b.parses,
	}
}

// RustType is the member's type: Option<runtime::filter::Ordered<i64>>,
// which names the runtime by the API crate's re-export (D43).
func (f FilterFieldInfo) RustType() string {
	return "Option<runtime::filter::" + f.Shape + "<" + f.Value.RustType + ">>"
}

// Decode is the runtime's function that reads the field's filters of a
// request: decode_ordered.
func (f FilterFieldInfo) Decode() string {
	return "decode_" + strings.ToLower(f.Shape)
}

// operatorVariants are the runtime's Operator variants by the wire's
// names.
var operatorVariants = map[string]string{
	"eq":       "Eq",
	"ne":       "Ne",
	"in":       "In",
	"notIn":    "NotIn",
	"gt":       "Gt",
	"gte":      "Gte",
	"lt":       "Lt",
	"lte":      "Lte",
	"contains": "Contains",
	"isNull":   "IsNull",
}

// SpecFields are the FieldSpec expressions of the filter's fields, one a
// field: FieldSpec::new("kind", &[Operator::Eq, Operator::Ne,
// Operator::In, Operator::NotIn]).
func (f FilterInfo) SpecFields() []string {
	fields := make([]string, len(f.Fields))
	for i, field := range f.Fields {
		operators := make([]string, len(field.Operators))
		for j, op := range field.Operators {
			operators[j] = "Operator::" + operatorVariants[op]
		}
		fields[i] = fmt.Sprintf("FieldSpec::new(%s, &[%s])", rustString(field.Name), strings.Join(operators, ", "))
	}
	return fields
}

// FieldNames are the filter's fields, by their JSON names.
func (f FilterInfo) FieldNames() []string {
	names := make([]string, len(f.Fields))
	for i, field := range f.Fields {
		names[i] = field.Name
	}
	return names
}

// filter fills the endpoint's filter from apigen's (D106). Each field's
// value is resolved as a query parameter of the field's type, so its
// ParamSpec and Rust type are the ones a query list of that type has; a
// date's and a decimal's is read by the scalar core's parse.
func (b *builder) filter(ep apigen.EndpointInfo, info *EndpointInfo, prefix string, fields map[string]string) error {
	if ep.Filter == nil {
		return nil
	}
	owner := ep.Namespace + "." + ep.Name
	filter := &FilterInfo{
		Argument:  ep.Filter.Argument,
		Field:     rustutil.Identifier(ep.Filter.Argument, "filter"),
		TypeName:  info.HandlerName + "Filter",
		Operation: ep.Filter.Operation,
	}
	if other, taken := fields[filter.Field]; taken {
		return fmt.Errorf("rustrestgen: operation %s arguments %s and %s are both the Rust field %s", owner, other, ep.Filter.Argument, filter.Field)
	}
	fields[filter.Field] = ep.Filter.Argument
	members := map[string]string{}
	for _, field := range ep.Filter.Fields {
		value, err := b.paramAs(field.Value.Param, inQuery, prefix, b.parsedScalar(field.Kind))
		if err != nil {
			return fmt.Errorf("rustrestgen cannot decode filter field %s of %s: %w", field.Name, owner, err)
		}
		for _, op := range field.Operators {
			if operatorVariants[op] == "" {
				return fmt.Errorf("rustrestgen: filter field %s of %s takes the operator %s, which the Rust runtime's filter reader does not know", field.Name, owner, op)
			}
		}
		member := rustutil.Identifier(field.Name, "field")
		if other, taken := members[member]; taken {
			return fmt.Errorf("rustrestgen: operation %s filter fields %s and %s are both the Rust field %s", owner, other, field.Name, member)
		}
		members[member] = field.Name
		filter.Fields = append(filter.Fields, FilterFieldInfo{
			Name:      field.Name,
			Field:     member,
			Kind:      field.Kind,
			Shape:     field.Shape(),
			Operators: field.Operators,
			Value:     value,
		})
	}
	info.Filter = filter
	return nil
}
