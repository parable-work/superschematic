package apigen

import (
	"fmt"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// FilterWireDescription says how a filter is written in a query string
// (D106), after FilterDescription in the OpenAPI document, whose
// deepObject parameter describes the bracket form but cannot say that a
// field's value alone is equality.
func FilterWireDescription() string {
	return fmt.Sprintf("In the query string `%[1]s[field]=value` is equality, as `%[1]s[field][eq]=value` is, "+
		"and `%[1]s[field][operator]=value` names an operator. A request holds at most %[2]d filters.",
		ir.FilterParam, ir.FilterMaxFilters)
}

// filterOpenAPIParameter is a filtered endpoint's filter in the OpenAPI
// document (D106): one query parameter, filter whatever the argument is
// called, of style deepObject and exploded, never required, whose schema
// is a closed object with a property per field a caller may filter by.
// Each field is a closed object of its operators: eq and the comparisons
// hold a value of the field's type, described as a query parameter of that
// type is (queryParamOpenAPISchema), in a list of 1 to
// ir.FilterMaxInValues of them, contains text and isNull a boolean. The
// object form has no way to write filter[field]=value, which is the eq
// member's: the parameter's and each field's description say so.
func filterOpenAPIParameter(filter *FilterInfo, scalarExamples, scalarDescriptions, scalarMap map[string]string, schema *ir.Schema, dependencies map[string]*ir.Schema) map[string]interface{} {
	fields := make(map[string]interface{}, len(filter.Fields))
	for _, field := range filter.Fields {
		// One value of the field: the schema of a query parameter of its
		// type.
		value := func() map[string]interface{} {
			return queryParamOpenAPISchema(field.Value.Param, scalarExamples, scalarDescriptions, scalarMap, schema, dependencies)
		}
		operators := make(map[string]interface{}, len(field.Operators))
		for _, op := range field.Operators {
			description := FilterOperatorDescription(op)
			switch op {
			case ir.FilterOpIn:
				operators[op] = map[string]interface{}{
					"type":        "array",
					"items":       value(),
					"minItems":    1,
					"maxItems":    ir.FilterMaxInValues,
					"description": description + " The query string separates them with commas.",
				}
			case ir.FilterOpContains:
				operators[op] = map[string]interface{}{"type": "string", "minLength": 1, "description": description}
			case ir.FilterOpIsNull:
				operators[op] = map[string]interface{}{"type": "boolean", "description": description}
			default:
				// The value's schema under the operator's description; a
				// component's reference moves into allOf to take it.
				operand := openAPIExtensionTarget(value())
				operand["description"] = description
				operators[op] = operand
			}
		}
		fields[field.Name] = map[string]interface{}{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           operators,
			"description": fmt.Sprintf("Filters by %[2]s: `%[1]s[%[2]s]=value` is equality, and `%[1]s[%[2]s][operator]=value` names one of %[3]s.",
				ir.FilterParam, field.Name, strings.Join(field.Operators, ", ")),
		}
	}
	return map[string]interface{}{
		"name":        ir.FilterParam,
		"in":          "query",
		"required":    false,
		"style":       "deepObject",
		"explode":     true,
		"description": FilterDescription(filter) + " " + FilterWireDescription(),
		"schema": map[string]interface{}{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           fields,
		},
	}
}
