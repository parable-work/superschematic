// Package filterparse reads the filters of a list operation (FilterBy,
// D106). On the wire a filter is the bracket form: filter[field]=value is
// equality, and filter[field][op]=value names an operator. The generated
// router reads a request's filters with Read against the operation's Spec,
// the fields a caller may filter by and each one's operators, and the
// implementation takes them typed, a member per field (Equals, Ordered,
// Text), which the route decodes each value into as an item of a query
// list of the field's type (DecodeEquals, DecodeOrdered, DecodeText).
// Members are ANDed. ne and notIn negate eq and in (D112): each keeps the
// items the other leaves out, an item whose field is null among them. The
// TypeScript runtime's filter.ts and the Rust
// runtime's filter module read the same, held to
// runtime/http/testdata/filter_parity.json.
package filterparse

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/runtime/http/go/apperror"
	"github.com/parable-work/superschematic/runtime/http/go/paging"
	"github.com/parable-work/superschematic/runtime/http/go/response"
)

// Param is the query parameter every filter travels under.
const Param = "filter"

const (
	// MaxFilters is the most filter parameters a request may hold.
	MaxFilters = 50

	// MaxInValues is the most values an in may list.
	MaxInValues = 100
)

// Operator is the comparison a filter makes.
type Operator string

// The operators, as the wire names them.
const (
	OperatorEq       Operator = "eq"
	OperatorNe       Operator = "ne"
	OperatorIn       Operator = "in"
	OperatorNotIn    Operator = "notIn"
	OperatorGT       Operator = "gt"
	OperatorGTE      Operator = "gte"
	OperatorLT       Operator = "lt"
	OperatorLTE      Operator = "lte"
	OperatorContains Operator = "contains"
	OperatorIsNull   Operator = "isNull"
)

// Operators is every operator, in the order a field lists its own and
// Parse answers them.
var Operators = []Operator{
	OperatorEq, OperatorNe, OperatorIn, OperatorNotIn, OperatorGT, OperatorGTE, OperatorLT, OperatorLTE, OperatorContains, OperatorIsNull,
}

// Spec is an operation's FilterBy as the router carries it: the fields a
// caller may filter by.
type Spec struct {
	Fields []FieldSpec
}

// FieldSpec is one field of a Spec: its name, the item's field's JSON
// name, and the operators it takes.
type FieldSpec struct {
	Name      string
	Operators []Operator
}

// field finds a spec's field by name.
func (s Spec) field(name string) (FieldSpec, int, bool) {
	for i, field := range s.Fields {
		if field.Name == name {
			return field, i, true
		}
	}
	return FieldSpec{}, -1, false
}

// names are the spec's fields, in its order.
func (s Spec) names() []string {
	names := make([]string, len(s.Fields))
	for i, field := range s.Fields {
		names[i] = field.Name
	}
	return names
}

// Filter is one filter a request holds: the field, the operator and its
// values as the query sent them. Every operator but in and notIn holds one
// value; those two hold their list, split at commas, each value trimmed.
// isNull's value is true or false.
type Filter struct {
	Field    string
	Operator Operator
	Values   []string
}

// Request is the filters of a request, in the spec's field order and,
// within a field, in the order of Operators.
type Request []Filter

// Of are the request's filters of one field.
func (r Request) Of(field string) []Filter {
	var out []Filter
	for _, filter := range r {
		if filter.Field == field {
			out = append(out, filter)
		}
	}
	return out
}

// refuse is the 400 of a filter parameter, as paging refuses a page's:
// worded as the TypeScript runtime words a refused query parameter.
func refuse(parameter, reason, validator string) *apperror.AppError {
	details := paging.Details{Location: "query", Parameter: parameter, Reason: reason}
	if validator != "" {
		details.Errors = []paging.RuleError{{Validator: validator, Message: reason}}
	}
	return apperror.BadRequestError(fmt.Sprintf("Invalid query parameter %s: %s", parameter, reason), nil).
		WithCode(response.ValidationCode).WithDetails(details)
}

// at is the parameter a field's refusal names: filter[field].
func at(field string) string {
	return Param + "[" + field + "]"
}

// Parse reads a request's filters from a query, the router's whole read.
// A key that is filter, or starts with filter[, is a filter parameter;
// every other key is left alone. The keys are read in ascending byte
// order, so a request with two faults is refused for the same one
// whatever order it sent them in, and a refusal is the 400
// *apperror.AppError, in this order:
//
//   - more than MaxFilters filter parameters, counting each occurrence, at
//     filter;
//   - a key that is not filter[field] or filter[field][operator], at the
//     key;
//   - a field the spec does not name, an operator the field does not take,
//     and a field's operator sent twice (filter[f] and filter[f][eq] are
//     one), each at filter[field];
//   - a value the operator does not take, at filter[field]: isNull's is
//     true or false, in's and notIn's is 1 to MaxInValues values, and
//     contains needs text.
//
// A value's own type is the route's to check, as it decodes it.
func Parse(query url.Values, spec Spec) (Request, *apperror.AppError) {
	var keys []string
	total := 0
	for key, values := range query {
		if key == Param || strings.HasPrefix(key, Param+"[") {
			keys = append(keys, key)
			total += len(values)
		}
	}
	if total > MaxFilters {
		return nil, refuse(Param, fmt.Sprintf("must hold at most %d filters", MaxFilters), "max")
	}
	sort.Strings(keys)

	type slot struct{ field, op int }
	found := map[slot]Filter{}
	for _, key := range keys {
		field, op, ok := bracketKey(key)
		if !ok {
			return nil, refuse(key, "expected filter[field] or filter[field][operator]", "")
		}
		fieldSpec, fieldIndex, named := spec.field(field)
		if !named {
			reason := "is not a field this operation filters by"
			if names := spec.names(); len(names) > 0 {
				reason += "; expected one of " + strings.Join(names, ", ")
			}
			return nil, refuse(at(field), reason, "")
		}
		operator := Operator(op)
		if op == "" {
			operator = OperatorEq
		}
		opIndex := slices.Index(Operators, operator)
		if opIndex < 0 || !slices.Contains(fieldSpec.Operators, operator) {
			return nil, refuse(at(field), fmt.Sprintf("takes no operator %s; expected one of %s", operator, joinOperators(fieldSpec.Operators)), "")
		}
		values := query[key]
		place := slot{fieldIndex, opIndex}
		if _, twice := found[place]; twice || len(values) != 1 {
			return nil, refuse(at(field), fmt.Sprintf("holds %s more than once", operator), "")
		}
		filter := Filter{Field: field, Operator: operator}
		switch operator {
		case OperatorIsNull:
			if values[0] != "true" && values[0] != "false" {
				return nil, refuse(at(field), "isNull expects true or false", "")
			}
			filter.Values = values
		case OperatorIn, OperatorNotIn:
			for _, part := range strings.Split(values[0], ",") {
				if part = strings.TrimSpace(part); part != "" {
					filter.Values = append(filter.Values, part)
				}
			}
			switch {
			case len(filter.Values) == 0:
				return nil, refuse(at(field), fmt.Sprintf("%s names no value", operator), "")
			case len(filter.Values) > MaxInValues:
				return nil, refuse(at(field), fmt.Sprintf("%s must hold at most %d values", operator, MaxInValues), "max")
			}
		case OperatorContains:
			if values[0] == "" {
				return nil, refuse(at(field), "contains needs text", "")
			}
			filter.Values = values
		default:
			filter.Values = values
		}
		found[place] = filter
	}

	slots := make([]slot, 0, len(found))
	for place := range found {
		slots = append(slots, place)
	}
	sort.Slice(slots, func(i, j int) bool {
		if slots[i].field != slots[j].field {
			return slots[i].field < slots[j].field
		}
		return slots[i].op < slots[j].op
	})
	request := make(Request, 0, len(slots))
	for _, place := range slots {
		request = append(request, found[place])
	}
	return request, nil
}

// joinOperators lists a field's operators as a refusal names them.
func joinOperators(operators []Operator) string {
	names := make([]string, len(operators))
	for i, operator := range operators {
		names[i] = string(operator)
	}
	return strings.Join(names, ", ")
}

// bracketKey reads filter[field] or filter[field][op]: the field, and the
// operator, empty when the key names none. ok is false for any other key:
// filter alone, an empty field or operator, a third bracket, or text
// outside the brackets. A field and an operator hold no bracket.
func bracketKey(key string) (field, op string, ok bool) {
	chain, found := strings.CutPrefix(key, Param)
	if !found || !strings.HasPrefix(chain, "[") || !strings.HasSuffix(chain, "]") {
		return "", "", false
	}
	segments := strings.Split(chain[1:len(chain)-1], "][")
	if len(segments) > 2 {
		return "", "", false
	}
	for _, segment := range segments {
		if segment == "" || strings.ContainsAny(segment, "[]") {
			return "", "", false
		}
	}
	if len(segments) == 2 {
		return segments[0], segments[1], true
	}
	return segments[0], "", true
}

// Read reads the request's filters (Parse) from its raw query, read as
// paging.QueryValues reads one. On a refusal it writes the 400 problem,
// with the request's id, and returns false.
func Read(w http.ResponseWriter, r *http.Request, spec Spec) (Request, bool) {
	request, refusal := Parse(paging.QueryValues(r.URL.RawQuery), spec)
	if refusal != nil {
		response.LoggedErrorWithDetails(w, r, refusal.HTTPStatus(), refusal.Message, refusal.ErrorCode, refusal.Details)
		return nil, false
	}
	return request, true
}

// Query writes filters as the bracket form's query parameters, the
// inverse of Parse: filter[field]=value for eq, filter[field][op]=value
// for any other operator, an in's and a notIn's values joined by commas. A
// filter of no value is left out. An SDK writes its typed filter's members with
// Equals.Filters, Ordered.Filters and Text.Filters and hands them here, so
// the wire's form is written in one place; Check tells it first which of
// them Parse would not read back.
func Query(filters []Filter) url.Values {
	values := url.Values{}
	for _, filter := range filters {
		if len(filter.Values) == 0 {
			continue
		}
		if filter.Operator == OperatorEq || filter.Operator == "" {
			values.Set(at(filter.Field), filter.Values[0])
			continue
		}
		values.Set(at(filter.Field)+"["+string(filter.Operator)+"]", strings.Join(filter.Values, ","))
	}
	return values
}

// CheckError is why Check refuses a filter. Parameter is where: a filter
// with its operator, filter[field][in], an in's value with its index
// besides, filter[field][in][2], or filter for a request's count.
// Validator names the rule and Reason says what it asks.
type CheckError struct {
	Parameter string
	Validator string
	Reason    string
}

// Error is the parameter and what it must be: filter[field][contains]
// needs text.
func (e *CheckError) Error() string {
	return e.Parameter + " " + e.Reason
}

// Check reports the first of filters that Query would write and Parse
// would refuse or read as something else, as a *CheckError, so an SDK
// refuses it before the request:
//
//   - more than MaxFilters filters that hold a value, at filter;
//   - an in or a notIn of more than MaxInValues values, at
//     filter[field][in] or filter[field][notIn];
//   - an in's or a notIn's value that is blank, which Parse drops, or
//     holds a comma, which Parse reads as two values, or leading or
//     trailing space, which Parse trims, at filter[field][in][index] or
//     filter[field][notIn][index];
//   - a contains of no text, at filter[field][contains].
//
// A filter of no value, an empty in among them, is left out by Query and
// is no fault. A value's own type stays the route's to check.
func Check(filters []Filter) error {
	count := 0
	for _, filter := range filters {
		if len(filter.Values) > 0 {
			count++
		}
	}
	if count > MaxFilters {
		return &CheckError{Parameter: Param, Validator: "max", Reason: fmt.Sprintf("must hold at most %d filters", MaxFilters)}
	}
	for _, filter := range filters {
		if len(filter.Values) == 0 {
			continue
		}
		name := at(filter.Field) + "[" + string(filter.Operator) + "]"
		switch filter.Operator {
		case OperatorIn, OperatorNotIn:
			if len(filter.Values) > MaxInValues {
				return &CheckError{Parameter: name, Validator: "max", Reason: fmt.Sprintf("must hold at most %d values", MaxInValues)}
			}
			for i, value := range filter.Values {
				item := fmt.Sprintf("%s[%d]", name, i)
				switch trimmed := strings.TrimSpace(value); {
				case trimmed == "":
					return &CheckError{Parameter: item, Validator: "required", Reason: "is required"}
				case trimmed != value || strings.Contains(value, ","):
					return &CheckError{Parameter: item, Validator: "pattern", Reason: "must not contain a comma or surrounding space"}
				}
			}
		case OperatorContains:
			if filter.Values[0] == "" {
				return &CheckError{Parameter: name, Validator: "minLength", Reason: "needs text"}
			}
		}
	}
	return nil
}
