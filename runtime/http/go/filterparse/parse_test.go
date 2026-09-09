package filterparse

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sortFilters sorts a Filter slice by field name so that test comparisons are
// deterministic regardless of url.Values map iteration order.
func sortFilters(fs []Filter) {
	sort.Slice(fs, func(i, j int) bool {
		return fs[i].Field < fs[j].Field
	})
}

func TestParseFilters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		query         string
		wantFilters   []Filter
		wantErrParams []string // Param fields of expected ParseErrors, in any order
		wantErrCount  int
	}{
		{
			name:  "default eq operator",
			query: "filter[location_type]=remote",
			wantFilters: []Filter{
				{Field: "location_type", Operator: OperatorEq, Values: []string{"remote"}},
			},
		},
		{
			name:  "in operator with comma-split values",
			query: "filter[country][in]=US,Canada",
			wantFilters: []Filter{
				{Field: "country", Operator: OperatorIn, Values: []string{"US", "Canada"}},
			},
		},
		{
			name:  "contains operator",
			query: "filter[title][contains]=Senior",
			wantFilters: []Filter{
				{Field: "title", Operator: OperatorContains, Values: []string{"Senior"}},
			},
		},
		{
			name:  "gte operator",
			query: "filter[hire_year][gte]=2024",
			wantFilters: []Filter{
				{Field: "hire_year", Operator: OperatorGTE, Values: []string{"2024"}},
			},
		},
		{
			name:  "multiple filters are AND-ed",
			query: "filter[a]=x&filter[b]=y",
			wantFilters: []Filter{
				{Field: "a", Operator: OperatorEq, Values: []string{"x"}},
				{Field: "b", Operator: OperatorEq, Values: []string{"y"}},
			},
		},
		{
			name:          "empty field name",
			query:         "filter[]=value",
			wantFilters:   nil,
			wantErrParams: []string{"filter[]"},
			wantErrCount:  1,
		},
		{
			name:          "unknown operator",
			query:         "filter[x][bad]=y",
			wantFilters:   nil,
			wantErrParams: []string{"filter[x][bad]"},
			wantErrCount:  1,
		},
		{
			name:          "too many bracket segments",
			query:         "filter[x][y][z]=v",
			wantFilters:   nil,
			wantErrParams: []string{"filter[x][y][z]"},
			wantErrCount:  1,
		},
		{
			// Semicolons in query strings are rejected by url.ParseQuery since Go 1.17.
			// Use a percent-encoded semicolon so the raw key reaches ParseFilters.
			name:          "invalid field name characters",
			query:         "filter[x%3B+DROP+TABLE]=v",
			wantFilters:   nil,
			wantErrParams: []string{"filter[x; DROP TABLE]"},
			wantErrCount:  1,
		},
		{
			name:        "no filter params",
			query:       "",
			wantFilters: nil,
		},
		{
			name:  "non-filter params are ignored",
			query: "teamId=abc&from=2025-01-01&filter[x]=y",
			wantFilters: []Filter{
				{Field: "x", Operator: OperatorEq, Values: []string{"y"}},
			},
		},
		{
			name:  "neq operator",
			query: "filter[status][neq]=inactive",
			wantFilters: []Filter{
				{Field: "status", Operator: OperatorNeq, Values: []string{"inactive"}},
			},
		},
		{
			name:  "gt operator",
			query: "filter[age][gt]=30",
			wantFilters: []Filter{
				{Field: "age", Operator: OperatorGT, Values: []string{"30"}},
			},
		},
		{
			name:  "lte operator",
			query: "filter[score][lte]=100",
			wantFilters: []Filter{
				{Field: "score", Operator: OperatorLTE, Values: []string{"100"}},
			},
		},
		{
			name:  "lt operator",
			query: "filter[price][lt]=50",
			wantFilters: []Filter{
				{Field: "price", Operator: OperatorLT, Values: []string{"50"}},
			},
		},
		{
			name:  "explicit eq operator",
			query: "filter[name][eq]=Alice",
			wantFilters: []Filter{
				{Field: "name", Operator: OperatorEq, Values: []string{"Alice"}},
			},
		},
		{
			name:          "multiple errors are all reported",
			query:         "filter[]=a&filter[x][bad]=b",
			wantFilters:   nil,
			wantErrParams: []string{"filter[]", "filter[x][bad]"},
			wantErrCount:  2,
		},
		{
			name:  "in operator with single value",
			query: "filter[role][in]=admin",
			wantFilters: []Filter{
				{Field: "role", Operator: OperatorIn, Values: []string{"admin"}},
			},
		},
		{
			name:  "in operator trims whitespace around commas",
			query: "filter[tags][in]=go, rust, python",
			wantFilters: []Filter{
				{Field: "tags", Operator: OperatorIn, Values: []string{"go", "rust", "python"}},
			},
		},
		{
			name:  "blank value produces single empty string",
			query: "filter[name]=",
			wantFilters: []Filter{
				{Field: "name", Operator: OperatorEq, Values: []string{""}},
			},
		},
		{
			name:  "in operator with blank value produces empty values",
			query: "filter[tags][in]=",
			wantFilters: []Filter{
				{Field: "tags", Operator: OperatorIn, Values: []string{}},
			},
		},
		{
			name:          "uppercase operator is rejected",
			query:         "filter[x][EQ]=v",
			wantFilters:   nil,
			wantErrParams: []string{"filter[x][EQ]"},
			wantErrCount:  1,
		},
		{
			name:  "in operator with leading and trailing commas",
			query: "filter[role][in]=,admin,",
			wantFilters: []Filter{
				{Field: "role", Operator: OperatorIn, Values: []string{"admin"}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			q, err := url.ParseQuery(tc.query)
			require.NoError(t, err)

			gotFilters, gotErrs := ParseFilters(q)

			// Sort both sides so comparison is order-independent.
			sortFilters(gotFilters)
			sortFilters(tc.wantFilters)

			assert.Equal(t, tc.wantFilters, gotFilters)

			if tc.wantErrCount > 0 {
				require.Len(t, gotErrs, tc.wantErrCount)

				gotParams := make([]string, len(gotErrs))
				for i, e := range gotErrs {
					gotParams[i] = e.Param
				}
				sort.Strings(gotParams)

				wantParams := make([]string, len(tc.wantErrParams))
				copy(wantParams, tc.wantErrParams)
				sort.Strings(wantParams)

				assert.Equal(t, wantParams, gotParams)
			} else {
				assert.Empty(t, gotErrs)
			}
		})
	}
}

func TestParseBracketKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		key       string
		wantField string
		wantOp    string
		wantErr   bool
	}{
		{
			name:      "field only",
			key:       "filter[location_type]",
			wantField: "location_type",
			wantOp:    "",
		},
		{
			name:      "field and operator",
			key:       "filter[country][in]",
			wantField: "country",
			wantOp:    "in",
		},
		{
			name:    "empty field",
			key:     "filter[]",
			wantErr: true,
		},
		{
			name:    "too many segments",
			key:     "filter[x][y][z]",
			wantErr: true,
		},
		{
			name:    "invalid field characters",
			key:     "filter[x; DROP TABLE]",
			wantErr: true,
		},
		{
			name:      "underscore and digits in field name",
			key:       "filter[field_name_123]",
			wantField: "field_name_123",
			wantOp:    "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			field, op, err := parseBracketKey(tc.key)

			if tc.wantErr {
				assert.NotNil(t, err)
				assert.Equal(t, tc.key, err.Param)
				assert.NotEmpty(t, err.Message)
				return
			}

			require.Nil(t, err)
			assert.Equal(t, tc.wantField, field)
			assert.Equal(t, tc.wantOp, op)
		})
	}
}

func TestParseOperator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input  string
		wantOp Operator
		wantOK bool
	}{
		{"", OperatorEq, true},
		{"eq", OperatorEq, true},
		{"in", OperatorIn, true},
		{"contains", OperatorContains, true},
		{"neq", OperatorNeq, true},
		{"gt", OperatorGT, true},
		{"gte", OperatorGTE, true},
		{"lt", OperatorLT, true},
		{"lte", OperatorLTE, true},
		{"bad", "", false},
		{"LIKE", "", false},
		{"between", "", false},
	}

	for _, tc := range tests {
		t.Run("op="+tc.input, func(t *testing.T) {
			t.Parallel()

			gotOp, gotOK := parseOperator(tc.input)
			assert.Equal(t, tc.wantOp, gotOp)
			assert.Equal(t, tc.wantOK, gotOK)
		})
	}
}

func TestValidOperators(t *testing.T) {
	t.Parallel()

	// Every constant must appear in ValidOperators exactly once.
	expected := []string{"eq", "in", "contains", "neq", "gt", "gte", "lt", "lte"}
	assert.Equal(t, expected, ValidOperators)

	// Roundtrip: every ValidOperator must be accepted by parseOperator.
	for _, opStr := range ValidOperators {
		_, ok := parseOperator(opStr)
		assert.True(t, ok, "ValidOperators contains %q which parseOperator rejects", opStr)
	}
}

func TestMaxFiltersLimit(t *testing.T) {
	t.Parallel()

	q := make(url.Values)
	for i := range MaxFilters + 5 {
		q.Set(fmt.Sprintf("filter[field%d]", i), "value")
	}

	filters, errs := ParseFilters(q)
	// Cap is enforced exactly at MaxFilters (not +1): we should retain all
	// filters up to the cap and NOT include one beyond it.
	assert.Len(t, filters, MaxFilters, "should retain exactly MaxFilters good filters")
	require.NotEmpty(t, errs)
	assert.Contains(t, errs[len(errs)-1].Message, "too many filter parameters")
}

func TestMaxInValuesLimit(t *testing.T) {
	t.Parallel()

	values := make([]string, MaxInValues+10)
	for i := range values {
		values[i] = fmt.Sprintf("v%d", i)
	}

	q := make(url.Values)
	q.Set("filter[x][in]", strings.Join(values, ","))

	filters, errs := ParseFilters(q)
	assert.Empty(t, filters, "should not produce a filter when in-values exceed limit")
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Message, "too many values")
}

// TestSerializeEmitsBracketNotation locks the wire format of the Serialize
// writer so SDK generators can rely on it as a reference implementation.
func TestSerializeEmitsBracketNotation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		filters []Filter
		want    url.Values
	}{
		{
			name: "eq operator emits bare field key",
			filters: []Filter{
				{Field: "location_type", Operator: OperatorEq, Values: []string{"remote"}},
			},
			want: url.Values{"filter[location_type]": []string{"remote"}},
		},
		{
			name: "empty operator defaults to eq",
			filters: []Filter{
				{Field: "status", Operator: "", Values: []string{"active"}},
			},
			want: url.Values{"filter[status]": []string{"active"}},
		},
		{
			name: "in operator comma-joins multiple values",
			filters: []Filter{
				{Field: "country", Operator: OperatorIn, Values: []string{"US", "Canada"}},
			},
			want: url.Values{"filter[country][in]": []string{"US,Canada"}},
		},
		{
			name: "empty Values slice is skipped",
			filters: []Filter{
				{Field: "status", Operator: OperatorEq, Values: []string{"active"}},
				{Field: "skip_me", Operator: OperatorIn, Values: []string{}},
			},
			want: url.Values{"filter[status]": []string{"active"}},
		},
		{
			name:    "nil filters returns nil",
			filters: nil,
			want:    nil,
		},
		{
			name: "comparison operators emit filter[field][op]",
			filters: []Filter{
				{Field: "hire_year", Operator: OperatorGTE, Values: []string{"2024"}},
			},
			want: url.Values{"filter[hire_year][gte]": []string{"2024"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, Serialize(tc.filters))
		})
	}
}

// TestSerializeParseRoundtrip is the primary cross-layer contract test.
// It asserts that any valid []Filter survives Serialize -> ParseFilters
// without data loss. A regression here means SDK consumers can silently
// lose values on the wire -- the class of bug that caused the Go SDK's
// in-operator data-loss blocker in an earlier review.
func TestSerializeParseRoundtrip(t *testing.T) {
	t.Parallel()

	input := []Filter{
		{Field: "location_type", Operator: OperatorEq, Values: []string{"remote"}},
		{Field: "country", Operator: OperatorIn, Values: []string{"US", "Canada", "Mexico"}},
		{Field: "hire_year", Operator: OperatorGTE, Values: []string{"2024"}},
		{Field: "title", Operator: OperatorContains, Values: []string{"Senior"}},
		{Field: "status", Operator: OperatorNeq, Values: []string{"inactive"}},
	}

	q := Serialize(input)

	got, errs := ParseFilters(q)
	require.Empty(t, errs, "roundtrip should produce no parse errors")
	require.Len(t, got, len(input))

	// Sort both sides for deterministic comparison.
	sortFilters(input)
	sortFilters(got)
	assert.Equal(t, input, got)
}
