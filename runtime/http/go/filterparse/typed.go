package filterparse

import (
	"encoding/json"

	"github.com/parable-work/superschematic/runtime/http/go/bodyargs"
	"github.com/parable-work/superschematic/runtime/schema/go/validate"
)

// A field's filter as an implementation takes it and an SDK sends it
// (D106): a member per operator, each nil, or empty, when the request
// holds none. Which type a field's is follows its operators: Equals for
// eq and in alone (a UUID, an enum, a boolean), Ordered for a number, a
// date and a time, which add gt, gte, lt and lte, and Text for text, which
// adds contains. IsNull is set only on a field that may be null: true
// keeps the rows whose field is null, false the others.

// Equals is the filter of a field compared for equality alone.
type Equals[T any] struct {
	Eq     *T
	In     []T
	IsNull *bool
}

// Ordered is the filter of a field whose values have an order: a number,
// a date or a time.
type Ordered[T any] struct {
	Eq     *T
	In     []T
	Gt     *T
	Gte    *T
	Lt     *T
	Lte    *T
	IsNull *bool
}

// Text is the filter of a field of text. Contains is the text a value
// holds, in any case.
type Text[T any] struct {
	Eq       *T
	In       []T
	Contains *string
	IsNull   *bool
}

// decoded is one field's filters of every shape, which each Decode
// function narrows to its type.
type decoded[T any] struct {
	any                  bool
	eq, gt, gte, lt, lte *T
	in                   []T
	contains             *string
	isNull               *bool
}

// decodeField reads the request's filters of one field: each value as an
// item of a query list of T (bodyargs.QueryItem), its error recorded at
// arg's name, filter[field]; contains is its text, and isNull the boolean
// Parse held it to.
func decodeField[T any](errs validate.ValidationErrors, request Request, field string, arg *bodyargs.Arg) decoded[T] {
	var out decoded[T]
	value := func(text string) *T {
		v := bodyargs.QueryItem[T](errs, arg, arg.Name(), text)
		return &v
	}
	for _, filter := range request.Of(field) {
		if len(filter.Values) == 0 {
			continue
		}
		out.any = true
		first := filter.Values[0]
		switch filter.Operator {
		case OperatorEq:
			out.eq = value(first)
		case OperatorIn:
			out.in = make([]T, len(filter.Values))
			for i, text := range filter.Values {
				out.in[i] = *value(text)
			}
		case OperatorGT:
			out.gt = value(first)
		case OperatorGTE:
			out.gte = value(first)
		case OperatorLT:
			out.lt = value(first)
		case OperatorLTE:
			out.lte = value(first)
		case OperatorContains:
			out.contains = &first
		case OperatorIsNull:
			isNull := first == "true"
			out.isNull = &isNull
		}
	}
	return out
}

// DecodeEquals reads the request's filters of a field compared for
// equality alone, or nil when it holds none. arg is the field's value as a
// query argument, named filter[field], where a value's error is recorded.
func DecodeEquals[T any](errs validate.ValidationErrors, request Request, field string, arg *bodyargs.Arg) *Equals[T] {
	d := decodeField[T](errs, request, field, arg)
	if !d.any {
		return nil
	}
	return &Equals[T]{Eq: d.eq, In: d.in, IsNull: d.isNull}
}

// DecodeOrdered reads the request's filters of an ordered field, or nil
// when it holds none.
func DecodeOrdered[T any](errs validate.ValidationErrors, request Request, field string, arg *bodyargs.Arg) *Ordered[T] {
	d := decodeField[T](errs, request, field, arg)
	if !d.any {
		return nil
	}
	return &Ordered[T]{Eq: d.eq, In: d.in, Gt: d.gt, Gte: d.gte, Lt: d.lt, Lte: d.lte, IsNull: d.isNull}
}

// DecodeText reads the request's filters of a field of text, or nil when
// it holds none.
func DecodeText[T any](errs validate.ValidationErrors, request Request, field string, arg *bodyargs.Arg) *Text[T] {
	d := decodeField[T](errs, request, field, arg)
	if !d.any {
		return nil
	}
	return &Text[T]{Eq: d.eq, In: d.in, Contains: d.contains, IsNull: d.isNull}
}

// text is a value as the query carries it: a JSON string's text, and any
// other value's JSON (a number, true or false).
func text(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	var s string
	if json.Unmarshal(data, &s) == nil {
		return s
	}
	return string(data)
}

// one is the filter of one value, or none for a nil one.
func one[T any](filters []Filter, field string, operator Operator, value *T) []Filter {
	if value == nil {
		return filters
	}
	return append(filters, Filter{Field: field, Operator: operator, Values: []string{text(*value)}})
}

// many is the filter of an in's values, or none for an empty list.
func many[T any](filters []Filter, field string, values []T) []Filter {
	if len(values) == 0 {
		return filters
	}
	texts := make([]string, len(values))
	for i, value := range values {
		texts[i] = text(value)
	}
	return append(filters, Filter{Field: field, Operator: OperatorIn, Values: texts})
}

// null is the isNull filter, or none when it is unset.
func null(filters []Filter, field string, isNull *bool) []Filter {
	if isNull == nil {
		return filters
	}
	return append(filters, Filter{Field: field, Operator: OperatorIsNull, Values: []string{text(*isNull)}})
}

// Filters are the filters the members set, of field, in the order of
// Operators: what an SDK hands Query. A nil filter is none.
func (f *Equals[T]) Filters(field string) []Filter {
	if f == nil {
		return nil
	}
	filters := one(nil, field, OperatorEq, f.Eq)
	filters = many(filters, field, f.In)
	return null(filters, field, f.IsNull)
}

// Filters are the filters the members set, of field, in the order of
// Operators. A nil filter is none.
func (f *Ordered[T]) Filters(field string) []Filter {
	if f == nil {
		return nil
	}
	filters := one(nil, field, OperatorEq, f.Eq)
	filters = many(filters, field, f.In)
	filters = one(filters, field, OperatorGT, f.Gt)
	filters = one(filters, field, OperatorGTE, f.Gte)
	filters = one(filters, field, OperatorLT, f.Lt)
	filters = one(filters, field, OperatorLTE, f.Lte)
	return null(filters, field, f.IsNull)
}

// Filters are the filters the members set, of field, in the order of
// Operators. A nil filter is none.
func (f *Text[T]) Filters(field string) []Filter {
	if f == nil {
		return nil
	}
	filters := one(nil, field, OperatorEq, f.Eq)
	filters = many(filters, field, f.In)
	filters = one(filters, field, OperatorContains, f.Contains)
	return null(filters, field, f.IsNull)
}
