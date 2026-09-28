package tsgen

import "testing"

const scalarFieldRulesService = "scalar-field-rules"

// Every shape a scalar field takes, each with rules of its own on top of
// the scalar's: single (required and optional), T[], T[][], a map value
// and a map value's list element. Identity.Name is a string of 2 to 80
// characters and Ordering.Rank an integer of at least 1. The schema
// runtimes do not walk maps, so the parity matrix has no map fields; this
// schema covers the map shapes.
const scalarFieldRulesSchemaJSON = `{
  "scalars": {
    "Generic.Int64": { "name": "Generic.Int64", "languagePrimitive": "number" },
    "Identity.Name": { "name": "Identity.Name", "languagePrimitive": "string" },
    "Ordering.Rank": { "name": "Ordering.Rank", "languagePrimitive": "number" }
  },
  "types": {
    "Sample": {
      "name": "Sample",
      "role": "EmbeddedStruct",
      "fields": [
        { "name": "amount", "typeRef": { "name": "Generic.Int64" }, "required": true, "validateMin": 0 },
        { "name": "name", "typeRef": { "name": "Identity.Name" }, "validateMaxLength": 5, "validatePattern": "^[a-z]+$" },
        { "name": "names", "typeRef": { "name": "Identity.Name", "isArray": true }, "validateMaxLength": 5 },
        { "name": "rankGrid", "typeRef": { "name": "Ordering.Rank", "isArray": true, "isArrayOfArrays": true }, "validateMax": 10 },
        { "name": "labels", "typeRef": { "name": "Identity.Name", "isMap": true }, "validateMaxLength": 5 },
        { "name": "limits", "typeRef": { "name": "Ordering.Rank", "isArray": true, "isMap": true }, "validateMax": 10 }
      ]
    }
  }
}`

// TestScalarFieldRules type-checks and runs validateSample: a scalar
// field's own rules follow the scalar's validation, as in the Go types, at
// every path the scalar's errors take. It dropped them:
// Validate<Generic.Int64, { min: 0 }> accepted -1. A rule checks only a
// value of its own JSON type, so a mistyped value is the scalar's one
// "type" error.
func TestScalarFieldRules(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-validator check in -short mode")
	}
	runSampleVectors(t, scalarFieldRulesService, scalarFieldRulesSchemaJSON, map[string]sampleVector{
		"at_bounds": {
			payload: `{"amount": 0, "name": "abcde", "names": ["ab"], "rankGrid": [[10]], "labels": {"k": "abcde"}, "limits": {"k": [1, 10]}}`,
			want:    map[string][]string{},
		},
		"field_rules": {
			payload: `{"amount": -1, "name": "abcdef", "names": ["abcdef"], "rankGrid": [[11]], "labels": {"k": "abcdef"}, "limits": {"k": [11]}}`,
			want: map[string][]string{
				"amount":         {"min"},
				"name":           {"maxLength"},
				"names[0]":       {"maxLength"},
				"rankGrid[0][0]": {"max"},
				"labels.k":       {"maxLength"},
				"limits.k[0]":    {"max"},
			},
		},
		"scalar_rules_then_field_rules": {
			payload: `{"amount": 0, "name": "A", "labels": {"k": "A"}, "limits": {"k": [0]}}`,
			want: map[string][]string{
				"name":        {"minLength", "pattern"},
				"labels.k":    {"minLength"},
				"limits.k[0]": {"min"},
			},
		},
		"wrong_types": {
			payload: `{"amount": "-5", "name": 42, "names": [7], "rankGrid": [["20"]], "labels": {"k": 7}, "limits": {"k": ["20"]}}`,
			want: map[string][]string{
				"amount":         {"type"},
				"name":           {"type"},
				"names[0]":       {"type"},
				"rankGrid[0][0]": {"type"},
				"labels.k":       {"type"},
				"limits.k[0]":    {"type"},
			},
		},
	})
}
