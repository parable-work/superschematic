package filterparse

import (
	"errors"
	"fmt"
	"testing"
)

// Check refuses, before a request, what Query would write and Parse would
// refuse or read as something else, naming the parameter, and passes
// everything Parse reads back as it was written.
func TestCheckRefusesWhatTheReaderWouldNotReadBack(t *testing.T) {
	many := func(n int) []string {
		values := make([]string, n)
		for i := range values {
			values[i] = fmt.Sprint(i)
		}
		return values
	}
	fields := func(n int) []Filter {
		filters := make([]Filter, n)
		for i := range filters {
			filters[i] = Filter{Field: fmt.Sprintf("f%d", i), Operator: OperatorEq, Values: []string{"x"}}
		}
		return filters
	}
	for _, test := range []struct {
		name    string
		filters []Filter
		want    *CheckError
	}{
		{name: "no filter"},
		{name: "every operator", filters: []Filter{
			{Field: "priority", Operator: OperatorEq, Values: []string{"3"}},
			{Field: "priority", Operator: OperatorIn, Values: []string{"3", "4"}},
			{Field: "priority", Operator: OperatorGT, Values: []string{"1"}},
			{Field: "title", Operator: OperatorContains, Values: []string{" a,b "}},
			{Field: "title", Operator: OperatorEq, Values: []string{"a, b"}},
			{Field: "dueOn", Operator: OperatorIsNull, Values: []string{"false"}},
		}},
		{name: "an empty in is left out", filters: []Filter{{Field: "stateId", Operator: OperatorIn}}},
		{name: "an in of the most values", filters: []Filter{{Field: "stateId", Operator: OperatorIn, Values: many(MaxInValues)}}},
		{
			name:    "an in of too many values",
			filters: []Filter{{Field: "stateId", Operator: OperatorIn, Values: many(MaxInValues + 1)}},
			want:    &CheckError{"filter[stateId][in]", "max", "must hold at most 100 values"},
		},
		{name: "an empty notIn is left out", filters: []Filter{{Field: "stateId", Operator: OperatorNotIn}}},
		{name: "a ne that holds a comma and a space", filters: []Filter{{Field: "title", Operator: OperatorNe, Values: []string{" a, b "}}}},
		{
			name:    "a notIn of too many values",
			filters: []Filter{{Field: "stateId", Operator: OperatorNotIn, Values: many(MaxInValues + 1)}},
			want:    &CheckError{"filter[stateId][notIn]", "max", "must hold at most 100 values"},
		},
		{
			name:    "a notIn's value with a comma",
			filters: []Filter{{Field: "title", Operator: OperatorNotIn, Values: []string{"a", "b,c"}}},
			want:    &CheckError{"filter[title][notIn][1]", "pattern", "must not contain a comma or surrounding space"},
		},
		{
			name:    "a notIn's blank value",
			filters: []Filter{{Field: "title", Operator: OperatorNotIn, Values: []string{"  "}}},
			want:    &CheckError{"filter[title][notIn][0]", "required", "is required"},
		},
		{
			name:    "an in's value with a comma",
			filters: []Filter{{Field: "title", Operator: OperatorIn, Values: []string{"a", "b,c"}}},
			want:    &CheckError{"filter[title][in][1]", "pattern", "must not contain a comma or surrounding space"},
		},
		{
			name:    "an in's value with a leading space",
			filters: []Filter{{Field: "title", Operator: OperatorIn, Values: []string{" a"}}},
			want:    &CheckError{"filter[title][in][0]", "pattern", "must not contain a comma or surrounding space"},
		},
		{
			name:    "an in's value with a trailing space",
			filters: []Filter{{Field: "title", Operator: OperatorIn, Values: []string{"a", "b", "c\t"}}},
			want:    &CheckError{"filter[title][in][2]", "pattern", "must not contain a comma or surrounding space"},
		},
		{
			name:    "an in's empty value",
			filters: []Filter{{Field: "title", Operator: OperatorIn, Values: []string{"a", ""}}},
			want:    &CheckError{"filter[title][in][1]", "required", "is required"},
		},
		{
			name:    "an in's blank value",
			filters: []Filter{{Field: "title", Operator: OperatorIn, Values: []string{"  "}}},
			want:    &CheckError{"filter[title][in][0]", "required", "is required"},
		},
		{
			name:    "a contains of no text",
			filters: []Filter{{Field: "title", Operator: OperatorContains, Values: []string{""}}},
			want:    &CheckError{"filter[title][contains]", "minLength", "needs text"},
		},
		{name: "the most filters", filters: append(fields(MaxFilters), Filter{Field: "none", Operator: OperatorIn})},
		{
			name:    "too many filters",
			filters: fields(MaxFilters + 1),
			want:    &CheckError{"filter", "max", "must hold at most 50 filters"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := Check(test.filters)
			if test.want == nil {
				if err != nil {
					t.Fatalf("Check = %v, want none", err)
				}
				// What Check passes, the reader reads back as it was
				// written.
				spec := Spec{}
				var written Request
				for _, filter := range test.filters {
					if _, _, named := spec.field(filter.Field); !named {
						spec.Fields = append(spec.Fields, FieldSpec{Name: filter.Field, Operators: Operators})
					}
					if len(filter.Values) > 0 {
						written = append(written, filter)
					}
				}
				read, refusal := Parse(Query(test.filters), spec)
				if refusal != nil {
					t.Fatalf("the reader refuses what Check passed: %v", refusal)
				}
				if len(read) != len(written) {
					t.Fatalf("the reader read %v, want %v", read, written)
				}
				for _, filter := range written {
					found := false
					for _, got := range read {
						if got.Field == filter.Field && got.Operator == filter.Operator {
							found = fmt.Sprint(got.Values) == fmt.Sprint(filter.Values)
						}
					}
					if !found {
						t.Errorf("the reader read %v, which lacks %v", read, filter)
					}
				}
				return
			}
			var refused *CheckError
			if !errors.As(err, &refused) || *refused != *test.want {
				t.Fatalf("Check = %v, want %+v", err, test.want)
			}
			if want := test.want.Parameter + " " + test.want.Reason; err.Error() != want {
				t.Errorf("error = %q, want %q", err, want)
			}
		})
	}
}
