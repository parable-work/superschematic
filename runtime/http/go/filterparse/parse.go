package filterparse

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

const (
	// MaxFilters is the maximum number of filter parameters accepted per request.
	MaxFilters = 50

	// MaxInValues is the maximum number of comma-separated values for the "in" operator.
	MaxInValues = 100
)

// Operator is the comparison operator for a filter.
type Operator string

const (
	OperatorEq       Operator = "eq"
	OperatorIn       Operator = "in"
	OperatorContains Operator = "contains"
	OperatorNeq      Operator = "neq"
	OperatorGT       Operator = "gt"
	OperatorGTE      Operator = "gte"
	OperatorLT       Operator = "lt"
	OperatorLTE      Operator = "lte"
)

// ValidOperators lists every operator accepted by ParseFilters, in definition order.
// Useful for documentation and SDK generation.
var ValidOperators = []string{
	string(OperatorEq),
	string(OperatorIn),
	string(OperatorContains),
	string(OperatorNeq),
	string(OperatorGT),
	string(OperatorGTE),
	string(OperatorLT),
	string(OperatorLTE),
}

// Filter represents a single parsed filter predicate.
//
// For single-value operators (eq, neq, contains, gt, gte, lt, lte) Values
// contains exactly one element — use Values[0]. For the "in" operator Values
// contains the comma-split list.
//
// Field names are validated against [a-zA-Z0-9_] by the parser, but whether a
// field is a valid dimension for the current request is the caller's
// responsibility. Callers should validate Field against an allowlist before
// using it in queries.
//
// When using OperatorContains to build SQL LIKE clauses, callers must escape
// the LIKE metacharacters '%' and '_' in Values[0] to prevent unintended
// pattern matching or performance degradation.
type Filter struct {
	Field    string
	Operator Operator
	Values   []string
}

// ParseError describes a single malformed filter parameter.
type ParseError struct {
	Param   string `json:"param"`
	Message string `json:"message"`
}

var fieldNameRE = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// ParseFilters scans q for keys of the form filter[field] or filter[field][op]
// and returns the parsed filters along with any validation errors encountered.
// All keys are inspected before returning so every problem is reported at once.
// Non-filter query parameters are ignored.
func ParseFilters(q url.Values) ([]Filter, []ParseError) {
	var filters []Filter
	var errs []ParseError

	for key, vals := range q {
		if !strings.HasPrefix(key, "filter[") {
			continue
		}

		field, opStr, parseErr := parseBracketKey(key)
		if parseErr != nil {
			errs = append(errs, *parseErr)
			continue
		}

		op, ok := parseOperator(opStr)
		if !ok {
			errs = append(errs, ParseError{
				Param:   key,
				Message: "unknown operator \"" + opStr + "\"",
			})
			continue
		}

		// url.Values maps each key to a slice; use the first value.
		raw := ""
		if len(vals) > 0 {
			raw = vals[0]
		}

		var values []string
		if op == OperatorIn {
			parts := strings.Split(raw, ",")
			values = make([]string, 0, len(parts))
			for _, p := range parts {
				if trimmed := strings.TrimSpace(p); trimmed != "" {
					values = append(values, trimmed)
				}
			}
			if len(values) > MaxInValues {
				errs = append(errs, ParseError{
					Param:   key,
					Message: fmt.Sprintf("too many values for \"in\" operator (maximum %d)", MaxInValues),
				})
				continue
			}
		} else {
			values = []string{raw}
		}

		if len(filters) >= MaxFilters {
			errs = append(errs, ParseError{
				Param:   key,
				Message: fmt.Sprintf("too many filter parameters (maximum %d)", MaxFilters),
			})
			break
		}

		filters = append(filters, Filter{
			Field:    field,
			Operator: op,
			Values:   values,
		})
	}

	// Sort by field name for deterministic output regardless of url.Values
	// map iteration order. Stable ordering improves SQL query cache hit rates
	// and test reproducibility.
	sort.Slice(filters, func(i, j int) bool {
		return filters[i].Field < filters[j].Field
	})

	return filters, errs
}

// parseBracketKey parses a filter query-parameter key into its field name and
// optional operator string. It returns a non-nil ParseError when the key is
// structurally invalid.
//
// Accepted forms:
//
//	filter[field]        → field="field", op=""
//	filter[field][op]    → field="field", op="op"
func parseBracketKey(key string) (field, op string, err *ParseError) {
	// Strip the leading "filter" prefix so we are left with the bracket chain.
	chain := key[len("filter"):]

	// Split on "][" after stripping the outer brackets.
	// chain looks like "[field]" or "[field][op]".
	if !strings.HasPrefix(chain, "[") || !strings.HasSuffix(chain, "]") {
		return "", "", &ParseError{Param: key, Message: "malformed filter parameter"}
	}

	// Remove the leading '[' and trailing ']', then split on ']['.
	inner := chain[1 : len(chain)-1]
	segments := strings.Split(inner, "][")

	switch len(segments) {
	case 1:
		field, op = segments[0], ""
	case 2:
		field, op = segments[0], segments[1]
	default:
		return "", "", &ParseError{
			Param:   key,
			Message: "too many bracket segments in filter parameter",
		}
	}

	if field == "" {
		return "", "", &ParseError{
			Param:   key,
			Message: "empty field name in filter parameter",
		}
	}

	if !fieldNameRE.MatchString(field) {
		return "", "", &ParseError{
			Param:   key,
			Message: "invalid characters in filter field name \"" + field + "\"",
		}
	}

	return field, op, nil
}

// parseOperator resolves an operator string to its typed constant.
// An empty string defaults to OperatorEq. It returns (op, true) on success
// and ("", false) when the string is not a recognised operator.
func parseOperator(s string) (Operator, bool) {
	switch Operator(s) {
	case "":
		return OperatorEq, true
	case OperatorEq,
		OperatorIn,
		OperatorContains,
		OperatorNeq,
		OperatorGT,
		OperatorGTE,
		OperatorLT,
		OperatorLTE:
		return Operator(s), true
	default:
		return "", false
	}
}

// Serialize is the canonical encoder for a []Filter into url.Values using the
// bracket-notation wire format. This is the inverse of ParseFilters — the
// invariant `ParseFilters(Serialize(fs)) ≈ fs` holds for any valid input
// (modulo map iteration order, which callers should not rely on).
//
// SDK generators should call this (or mirror it exactly) so the wire contract
// is defined in one place. Divergence has caused silent data loss before:
// emitting repeated keys (`Add` per value) loses values because ParseFilters
// reads only vals[0].
//
// Rules encoded here:
//   - `eq`: emit `filter[field]=values[0]`
//   - other ops: emit `filter[field][op]=strings.Join(values, ",")`
//   - filters with empty Values are skipped (meaningless empty predicate)
func Serialize(filters []Filter) url.Values {
	if len(filters) == 0 {
		return nil
	}
	q := url.Values{}
	for _, f := range filters {
		if len(f.Values) == 0 {
			continue
		}
		if f.Operator == OperatorEq || f.Operator == "" {
			q.Set("filter["+f.Field+"]", f.Values[0])
		} else {
			q.Set("filter["+f.Field+"]["+string(f.Operator)+"]", strings.Join(f.Values, ","))
		}
	}
	return q
}
