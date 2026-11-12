package filterparse

import (
	"encoding/json"
	"net/url"
	"reflect"
	"testing"

	"github.com/parable-work/superschematic/runtime/http/go/bodyargs"
	"github.com/parable-work/superschematic/runtime/schema/go/validate"
)

type status string

func ptr[T any](v T) *T { return &v }

// A route decodes a field's filters into its typed member: each value as
// an item of a query list of the field's type, nil for a field the
// request does not filter by.
func TestDecodeReadsEachFieldTyped(t *testing.T) {
	spec := Spec{Fields: []FieldSpec{
		{Name: "status", Operators: []Operator{OperatorEq, OperatorIn}},
		{Name: "priority", Operators: []Operator{OperatorEq, OperatorIn, OperatorGT, OperatorGTE, OperatorLT, OperatorLTE, OperatorIsNull}},
		{Name: "title", Operators: []Operator{OperatorEq, OperatorIn, OperatorContains}},
		{Name: "done", Operators: []Operator{OperatorEq, OperatorIn}},
	}}
	query, err := url.ParseQuery("filter[status][in]=open,done&filter[priority][gte]=2&filter[priority][lt]=5&filter[priority][isNull]=false&filter[title][contains]=crash&filter[title]=a,b&filter[done]=true")
	if err != nil {
		t.Fatal(err)
	}
	request, refusal := Parse(query, spec)
	if refusal != nil {
		t.Fatal(refusal)
	}
	errs := validate.NewValidationErrors()
	gotStatus := DecodeEquals[status](errs, request, "status", bodyargs.NewArg("filter[status]", bodyargs.String))
	gotPriority := DecodeOrdered[int64](errs, request, "priority", bodyargs.NewArg("filter[priority]", bodyargs.Integer))
	gotTitle := DecodeText[string](errs, request, "title", bodyargs.NewArg("filter[title]", bodyargs.String))
	gotDone := DecodeEquals[bool](errs, request, "done", bodyargs.NewArg("filter[done]", bodyargs.Boolean))
	gotOther := DecodeEquals[string](errs, request, "other", bodyargs.NewArg("filter[other]", bodyargs.String))
	if errs.HasErrors() {
		t.Fatalf("errors: %v", errs)
	}
	if want := (&Equals[status]{In: []status{"open", "done"}}); !reflect.DeepEqual(gotStatus, want) {
		t.Errorf("status = %+v, want %+v", gotStatus, want)
	}
	if want := (&Ordered[int64]{Gte: ptr(int64(2)), Lt: ptr(int64(5)), IsNull: ptr(false)}); !reflect.DeepEqual(gotPriority, want) {
		t.Errorf("priority = %+v, want %+v", gotPriority, want)
	}
	if want := (&Text[string]{Eq: ptr("a,b"), Contains: ptr("crash")}); !reflect.DeepEqual(gotTitle, want) {
		t.Errorf("title = %+v, want %+v", gotTitle, want)
	}
	if want := (&Equals[bool]{Eq: ptr(true)}); !reflect.DeepEqual(gotDone, want) {
		t.Errorf("done = %+v, want %+v", gotDone, want)
	}
	if gotOther != nil {
		t.Errorf("a field the request does not filter by = %+v, want nil", gotOther)
	}
}

// A value its type refuses is one error at filter[field], named as a
// query list's item's is.
func TestDecodeRefusesAValueItsTypeRefuses(t *testing.T) {
	spec := Spec{Fields: []FieldSpec{{Name: "priority", Operators: []Operator{OperatorEq, OperatorIn, OperatorGT}}}}
	for _, test := range []struct{ query, validator, message string }{
		{"filter[priority]=high", "type", "expected an integer"},
		{"filter[priority][in]=1,two", "type", "expected an integer"},
		{"filter[priority][gt]=1.5", "type", "expected an integer"},
		{"filter[priority]=11", "max", "must be at most 10"},
	} {
		query, err := url.ParseQuery(test.query)
		if err != nil {
			t.Fatal(err)
		}
		request, refusal := Parse(query, spec)
		if refusal != nil {
			t.Fatalf("%s: %v", test.query, refusal)
		}
		errs := validate.NewValidationErrors()
		DecodeOrdered[int64](errs, request, "priority", bodyargs.NewArg("filter[priority]", bodyargs.Integer, bodyargs.Max(10)))
		data, err := json.Marshal(errs)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string][]validate.ValidationError
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("%s: errors %s: %v", test.query, data, err)
		}
		at := got["filter[priority]"]
		if len(got) != 1 || len(at) != 1 || at[0].Validator != test.validator || at[0].Message != test.message {
			t.Errorf("%s: errors %s, want %s %q at filter[priority]", test.query, data, test.validator, test.message)
		}
	}
}

// A typed filter writes the filters its members set, in the operators'
// order, each value as the query carries it, and Query writes them as the
// bracket form.
func TestTypedFiltersWriteTheBracketForm(t *testing.T) {
	var none *Equals[string]
	if got := none.Filters("status"); got != nil {
		t.Errorf("a nil filter = %v, want none", got)
	}
	var filters []Filter
	filters = append(filters, (&Equals[status]{Eq: ptr(status("open")), In: []status{"open", "done"}, IsNull: ptr(true)}).Filters("status")...)
	filters = append(filters, (&Ordered[int64]{Gt: ptr(int64(1)), Gte: ptr(int64(2)), Lt: ptr(int64(8)), Lte: ptr(int64(9))}).Filters("priority")...)
	filters = append(filters, (&Ordered[float64]{Eq: ptr(1.5)}).Filters("score")...)
	filters = append(filters, (&Text[string]{Contains: ptr("crash on save")}).Filters("title")...)
	filters = append(filters, (&Equals[bool]{Eq: ptr(false)}).Filters("done")...)
	got := Query(filters)
	want := url.Values{
		"filter[status]":          {"open"},
		"filter[status][in]":      {"open,done"},
		"filter[status][isNull]":  {"true"},
		"filter[priority][gt]":    {"1"},
		"filter[priority][gte]":   {"2"},
		"filter[priority][lt]":    {"8"},
		"filter[priority][lte]":   {"9"},
		"filter[score]":           {"1.5"},
		"filter[title][contains]": {"crash on save"},
		"filter[done]":            {"false"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("query = %v, want %v", got, want)
	}
	if len(Query(nil)) != 0 || len(Query([]Filter{{Field: "status", Operator: OperatorIn}})) != 0 {
		t.Error("a filter of no value wrote a parameter")
	}
}
