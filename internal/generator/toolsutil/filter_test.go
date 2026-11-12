package toolsutil_test

import (
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/toolsutil"
	"github.com/parable-work/superschematic/internal/loader"
)

// pagedAPI is the API output of fixture-paged-api, whose listParts takes a
// filter on a field of every kind (D106).
func pagedAPI(t *testing.T) *apigen.APIOutput {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-paged-api"))
	if err != nil {
		t.Fatalf("load fixture-paged-api: %v", err)
	}
	output, err := apigen.Generate(schema, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: "fixture-paged-api"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return output
}

// encode is a property as the tool documents write it.
func encode(t *testing.T, property toolsutil.JSONSchemaProperty) string {
	t.Helper()
	data, err := json.Marshal(property)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestFilterToolParameter: a filtered operation's tool takes its filter as
// one optional argument under the argument's name (D106), beside its sort
// and its page: a closed object with a member per field of FilterBy, each
// either a value of the field's type, for equality, or a closed object of
// the field's operators. eq and the comparisons hold a value, with the
// scalar's key; in a list of 1 to 100 of them; contains non-empty text;
// isNull a boolean, on a field that may be null alone. The argument's
// description lists the fields. A tool of an operation without a FilterBy
// has no such argument.
func TestFilterToolParameter(t *testing.T) {
	output := pagedAPI(t)
	tools := map[string]toolSchema{}
	for _, tool := range buildToolSchemas(t, output) {
		tools[tool.Name] = tool
	}
	parts := tools["catalog.listParts"].Parameters
	if got := slices.Sorted(maps.Keys(parts.Properties)); !slices.Equal(got, []string{"cursor", "filter", "limit", "sort"}) || len(parts.Required) != 0 {
		t.Fatalf("listParts's arguments = %v, required %v", got, parts.Required)
	}
	if _, ok := tools["catalog.listItems"].Parameters.Properties["filter"]; ok {
		t.Error("listItems, which has no FilterBy, takes a filter")
	}

	filter := parts.Properties["filter"]
	wantDescription := "The rows the list keeps: each member is a field (id, name, kind, quantity, weight, price, discontinued, madeAt, retiredOn, supplierId) " +
		"holding a value, for equality, or an object of operators, and all of them hold at once."
	if filter.Type != "object" || filter.Description != wantDescription || filter.Nullable ||
		!strings.HasPrefix(encode(t, filter), `{"additionalProperties":false,"type":"object","description":`) {
		t.Errorf("filter = %s", encode(t, filter))
	}
	operators := map[string]string{}
	for name, field := range filter.Properties {
		if field.Type != "" || len(field.OneOf) != 2 || field.OneOf[1].Type != "object" || strings.Contains(encode(t, field), `"type":""`) {
			t.Errorf("%s is not a value or an object of operators: %s", name, encode(t, field))
			continue
		}
		operators[name] = strings.Join(slices.Sorted(maps.Keys(field.OneOf[1].Properties)), " ")
	}
	ordered := "eq gt gte in lt lte ne notIn"
	wantOperators := map[string]string{
		"id": "eq in ne notIn", "name": "contains eq in ne notIn", "kind": "eq in ne notIn", "quantity": ordered, "weight": ordered, "price": ordered,
		"discontinued": "eq in ne notIn", "madeAt": ordered, "retiredOn": "eq gt gte in isNull lt lte ne notIn", "supplierId": "eq in isNull ne notIn",
	}
	if !maps.Equal(operators, wantOperators) {
		t.Errorf("operators = %v, want %v", operators, wantOperators)
	}

	in := "The field is one of the values, at most 100."
	ne := "The field is not the value: every item eq leaves out, one whose field is null among them."
	notIn := "The field is none of the values, at most 100: every item in leaves out, one whose field is null among them."
	for name, want := range map[string]string{
		// An enum lists its values, alone, as eq and as each of an in.
		"kind": `{"description":"Filters by kind: a value it equals, or its operators (eq, ne, in, notIn).","oneOf":[` +
			`{"type":"string","enum":["bolt","nut","washer"]},` +
			`{"additionalProperties":false,"type":"object","properties":{` +
			`"eq":{"type":"string","description":"The field equals the value.","enum":["bolt","nut","washer"]},` +
			`"in":{"type":"array","description":"` + in + `","minItems":1,"maxItems":100,"items":{"type":"string","enum":["bolt","nut","washer"]}},` +
			`"ne":{"type":"string","description":"` + ne + `","enum":["bolt","nut","washer"]},` +
			`"notIn":{"type":"array","description":"` + notIn + `","minItems":1,"maxItems":100,"items":{"type":"string","enum":["bolt","nut","washer"]}}}}]}`,
		// Text adds contains, whose value is text of its own.
		"name": `{"description":"Filters by name: a value it equals, or its operators (eq, ne, in, notIn, contains).","oneOf":[` +
			`{"type":"string"},` +
			`{"additionalProperties":false,"type":"object","properties":{` +
			`"contains":{"type":"string","description":"The field's text holds the value, in any case.","minLength":1},` +
			`"eq":{"type":"string","description":"The field equals the value."},` +
			`"in":{"type":"array","description":"` + in + `","minItems":1,"maxItems":100,"items":{"type":"string"}},` +
			`"ne":{"type":"string","description":"` + ne + `"},` +
			`"notIn":{"type":"array","description":"` + notIn + `","minItems":1,"maxItems":100,"items":{"type":"string"}}}}]}`,
		// A scalar keeps its key and its constraints on every value, and a
		// field that may be null adds isNull.
		"supplierId": `{"description":"Filters by supplierId: a value it equals, or its operators (eq, ne, in, notIn, isNull).","oneOf":[` +
			`{"x-superschematic-scalar":"Identity.UUID","type":"string","pattern":"` + uuidPattern + `"},` +
			`{"additionalProperties":false,"type":"object","properties":{` +
			`"eq":{"x-superschematic-scalar":"Identity.UUID","type":"string","description":"The field equals the value.","pattern":"` + uuidPattern + `"},` +
			`"in":{"type":"array","description":"` + in + `","minItems":1,"maxItems":100,"items":{"x-superschematic-scalar":"Identity.UUID","type":"string","pattern":"` + uuidPattern + `"}},` +
			`"isNull":{"type":"boolean","description":"true keeps the items whose field is null, false the others."},` +
			`"ne":{"x-superschematic-scalar":"Identity.UUID","type":"string","description":"` + ne + `","pattern":"` + uuidPattern + `"},` +
			`"notIn":{"type":"array","description":"` + notIn + `","minItems":1,"maxItems":100,"items":{"x-superschematic-scalar":"Identity.UUID","type":"string","pattern":"` + uuidPattern + `"}}}}]}`,
	} {
		if got := encode(t, filter.Properties[name]); got != want {
			t.Errorf("%s =\n%s\nwant\n%s", name, got, want)
		}
	}
	// An ordered field's comparisons each hold a value of its type.
	quantity := filter.Properties["quantity"].OneOf[1].Properties
	for op, description := range map[string]string{
		"gt": "The field is greater than the value.", "gte": "The field is the value or greater.",
		"lt": "The field is less than the value.", "lte": "The field is the value or less.",
	} {
		want := `{"x-superschematic-scalar":"Generic.Int64","type":"integer","description":"` + description + `","minimum":-9007199254740991,"maximum":9007199254740991}`
		if got := encode(t, quantity[op]); got != want {
			t.Errorf("quantity.%s =\n%s\nwant\n%s", op, got, want)
		}
	}
}

// uuidPattern is Identity.UUID's pattern as a JSON string holds it.
const uuidPattern = `^([0-9A-Za-z]{1,22}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`

// TestFilterToolParameterUnderItsArgumentsName: the tool's argument takes
// the filter's argument's name, through the SDK's key function, whatever
// the wire's parameter is called (D106); the vendor keys are the build's,
// and none when the build names none.
func TestFilterToolParameterUnderItsArgumentsName(t *testing.T) {
	output := pagedAPI(t)
	var filter apigen.FilterInfo
	for _, endpoint := range output.Endpoints {
		if endpoint.Name == "listParts" {
			filter = *endpoint.Filter
		}
	}
	filter.Argument = "only_where"
	camel := func(name, _ string) string {
		head, tail, _ := strings.Cut(name, "_")
		return head + strings.ToUpper(tail[:1]) + tail[1:]
	}
	build := func(keys apigen.ToolKeys) toolsutil.JSONSchemaObject {
		return toolsutil.BuildParametersSchema(nil, nil, false, "", nil, nil, output.TypeEnums, nil, false, output.Scalars, camel, keys, nil, &filter)
	}
	schema := build(apigen.ToolKeys{Scalar: "x-vendor-scalar"})
	property, ok := schema.Properties["onlyWhere"]
	if !ok || len(schema.Properties) != 1 || len(schema.Required) != 0 {
		t.Fatalf("arguments = %v, required %v", slices.Sorted(maps.Keys(schema.Properties)), schema.Required)
	}
	if got, want := encode(t, property.Properties["madeAt"].OneOf[0]), `{"x-vendor-scalar":"Temporal.DateTime","type":"string","format":"date-time"}`; got != want {
		t.Errorf("madeAt's value = %s, want %s", got, want)
	}
	if got := encode(t, build(apigen.ToolKeys{}).Properties["onlyWhere"]); strings.Contains(got, "x-") {
		t.Errorf("an unkeyed filter holds a vendor key: %s", got)
	}
	if holding := schema.Holds(); holding.Reference || holding.Search || holding.Sort {
		t.Errorf("a filter holds %+v", holding)
	}
}
