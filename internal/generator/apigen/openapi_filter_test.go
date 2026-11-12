package apigen_test

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	ir "github.com/parable-work/superschematic/ir"
)

// openAPIParameters are an operation's parameters in fixture-paged-api's
// OpenAPI document, by name, and their names in the document's order.
func openAPIParameters(t *testing.T, document map[string]any, path string) (map[string]map[string]any, []string) {
	t.Helper()
	operation, ok := document["paths"].(map[string]any)[path].(map[string]any)["get"].(map[string]any)
	if !ok {
		t.Fatalf("the document has no GET %s", path)
	}
	parameters := map[string]map[string]any{}
	var names []string
	for _, entry := range operation["parameters"].([]any) {
		parameter := entry.(map[string]any)
		name := parameter["name"].(string)
		parameters[name] = parameter
		names = append(names, name)
	}
	return parameters, names
}

// undescribed is a schema without its description, which an operator of a
// filter replaces with its own.
func undescribed(schema map[string]any) map[string]any {
	out := maps.Clone(schema)
	delete(out, "description")
	return out
}

// TestOpenAPIFilterParameter: a filtered operation's filter is one query
// parameter of the OpenAPI document (D106), filter, of style deepObject
// and exploded, not required, between the operation's own parameters and
// its page's. Its schema is a closed object with a property per field of
// FilterBy, each a closed object of the field's operators: eq and the
// comparisons hold the schema a query parameter of the field's type gets,
// in a list of 1 to 100 of it, contains non-empty text and isNull a
// boolean. The parameter's description lists the fields and says
// filter[field]=value is equality, and each field's says the same of its
// own. An operation without a FilterBy has no such parameter.
func TestOpenAPIFilterParameter(t *testing.T) {
	output := generatePagedAPI(t)
	var document map[string]any
	if err := json.Unmarshal([]byte(output.OpenAPISpecRaw), &document); err != nil {
		t.Fatal(err)
	}
	parameters, names := openAPIParameters(t, document, "/api/parts")
	if !reflect.DeepEqual(names, []string{"sort", "filter", "cursor", "limit"}) {
		t.Fatalf("listParts's parameters = %v", names)
	}
	filter := parameters["filter"]
	if filter["in"] != "query" || filter["style"] != "deepObject" || filter["explode"] != true || filter["required"] != false {
		t.Errorf("filter = %v", undescribed(filter))
	}
	var info *apigen.FilterInfo
	for _, endpoint := range output.Endpoints {
		if endpoint.Name == "listParts" {
			info = endpoint.Filter
		}
	}
	description, _ := filter["description"].(string)
	if description != apigen.FilterDescription(info)+" "+apigen.FilterWireDescription() ||
		!strings.Contains(description, "(id, name, kind, quantity, weight, price, discontinued, madeAt, retiredOn, supplierId)") ||
		!strings.Contains(description, "`filter[field]=value` is equality") || !strings.Contains(description, "`filter[field][operator]=value`") ||
		!strings.Contains(description, "at most 50 filters") {
		t.Errorf("filter's description = %q", description)
	}
	schema := filter["schema"].(map[string]any)
	if schema["type"] != "object" || schema["additionalProperties"] != false || len(schema) != 3 {
		t.Errorf("filter's schema = %v", schema)
	}
	fields := schema["properties"].(map[string]any)
	if got := slices.Sorted(maps.Keys(fields)); !reflect.DeepEqual(got, slices.Sorted(slices.Values(info.FieldNames()))) {
		t.Fatalf("filter's fields = %v, want %v", got, info.FieldNames())
	}

	// The schema a query parameter of each field's type gets: listItems's
	// own parameters for a UUID and a boolean, and the document's schemas
	// of the other types, as PartView's required fields hold them.
	items, _ := openAPIParameters(t, document, "/api/items")
	components := document["components"].(map[string]any)["schemas"].(map[string]any)
	part := components["PartView"].(map[string]any)["properties"].(map[string]any)
	uuid := items["ids"]["schema"].(map[string]any)["items"].(map[string]any)
	values := map[string]map[string]any{
		"id":           uuid,
		"name":         part["name"].(map[string]any),
		"kind":         {"$ref": "#/components/schemas/PartKind"},
		"quantity":     part["quantity"].(map[string]any),
		"weight":       part["weight"].(map[string]any),
		"price":        part["price"].(map[string]any),
		"discontinued": items["inStock"]["schema"].(map[string]any),
		"madeAt":       part["madeAt"].(map[string]any),
		// A filter's value is never null: the nullable fields' schemas
		// without the flag.
		"retiredOn":  {"type": "string", "format": "date", "maxLength": float64(40), "description": part["retiredOn"].(map[string]any)["description"]},
		"supplierId": uuid,
	}
	if _, ok := components["PartKind"]; !ok {
		t.Error("the document has no PartKind component, which the filter's kind references")
	}

	for _, field := range info.Fields {
		property := fields[field.Name].(map[string]any)
		if property["type"] != "object" || property["additionalProperties"] != false || len(property) != 4 {
			t.Errorf("%s = %v", field.Name, property)
		}
		wantDescription := "Filters by " + field.Name + ": `filter[" + field.Name + "]=value` is equality, and `filter[" + field.Name + "][operator]=value` names one of " + strings.Join(field.Operators, ", ") + "."
		if property["description"] != wantDescription {
			t.Errorf("%s's description = %q, want %q", field.Name, property["description"], wantDescription)
		}
		operators := property["properties"].(map[string]any)
		if got := slices.Sorted(maps.Keys(operators)); !reflect.DeepEqual(got, slices.Sorted(slices.Values(field.Operators))) {
			t.Errorf("%s's operators = %v, want %v", field.Name, got, field.Operators)
			continue
		}
		value := values[field.Name]
		for _, op := range field.Operators {
			operand := operators[op].(map[string]any)
			var want map[string]any
			description := apigen.FilterOperatorDescription(op)
			switch op {
			case ir.FilterOpIn, ir.FilterOpNotIn:
				want = map[string]any{"type": "array", "items": value, "minItems": float64(1), "maxItems": float64(ir.FilterMaxInValues)}
				description += " The query string separates them with commas."
			case ir.FilterOpContains:
				want = map[string]any{"type": "string", "minLength": float64(1)}
			case ir.FilterOpIsNull:
				want = map[string]any{"type": "boolean"}
			default:
				want = undescribed(value)
				if ref, ok := want["$ref"]; ok {
					// A component's reference takes no sibling: it moves
					// into allOf.
					want = map[string]any{"allOf": []any{map[string]any{"$ref": ref}}}
				}
			}
			if operand["description"] != description || description == "" {
				t.Errorf("%s.%s's description = %q, want %q", field.Name, op, operand["description"], description)
			}
			if got := undescribed(operand); !reflect.DeepEqual(got, want) {
				t.Errorf("%s.%s = %v, want %v", field.Name, op, got, want)
			}
		}
	}
	// The operators follow the kinds: isNull on the two fields that may be
	// null, contains on text alone.
	for name, want := range map[string]string{
		"name": "contains eq in ne notIn", "kind": "eq in ne notIn", "quantity": "eq gt gte in lt lte ne notIn",
		"retiredOn": "eq gt gte in isNull lt lte ne notIn", "supplierId": "eq in isNull ne notIn",
	} {
		operators := fields[name].(map[string]any)["properties"].(map[string]any)
		if got := strings.Join(slices.Sorted(maps.Keys(operators)), " "); got != want {
			t.Errorf("%s's operators = %s, want %s", name, got, want)
		}
	}

	// listItems has no FilterBy.
	if _, ok := items["filter"]; ok {
		t.Errorf("listItems has a filter parameter: %v", items["filter"])
	}
}
