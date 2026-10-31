package apigen_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestOpenAPIBehaviorOperations: fixture-workflow-api's OpenAPI document
// holds each behavior operation as any operation (D103), at its route
// under the table's key, with its input and result types, and with the
// problems the behaviors' core answers it with beside the 400 and 500 of
// every route; the fields a behavior owns are readOnly in the table's
// component, which getOrder answers, and in a @source view's that projects
// them, and in no input.
func TestOpenAPIBehaviorOperations(t *testing.T) {
	load := func(name string) *ir.Schema {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return schema
	}
	schema, db := load("fixture-workflow-api"), load("fixture-workflow-db")
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   "fixture-workflow-api",
		ModulePath:   "example.com/schemas/api/fixture-workflow-api",
		TypesModule:  "example.com/schemas/types/go/fixture-workflow-api",
		Dependencies: map[string]*ir.Schema{"fixture-workflow-db": db},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var doc struct {
		Paths      map[string]map[string]map[string]any `json:"paths"`
		Components struct {
			Schemas map[string]map[string]any `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal([]byte(output.OpenAPISpecRaw), &doc); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ path, operationID, input, handle string }{
		{"/api/order/{id}/status/transition", "OrderStatusTransitionHandler", "OrderStatusTransitionInput", "order_status_transition"},
		{"/api/shipment/{id}/status/transition", "ShipmentStatusTransitionHandler", "ShipmentStatusTransitionInput", "shipment_status_transition"},
		{"/api/shipment/{id}/payment-status/transition", "ShipmentPaymentStatusTransitionHandler", "ShipmentPaymentStatusTransitionInput", "shipment_payment_status_transition"},
	} {
		operation := doc.Paths[tc.path]["post"]
		if operation == nil {
			t.Errorf("no POST %s: %v", tc.path, keys(doc.Paths))
			continue
		}
		if operation["operationId"] != tc.operationID {
			t.Errorf("%s: operationId = %v, want %s", tc.path, operation["operationId"], tc.operationID)
		}
		var parameters []string
		for _, p := range operation["parameters"].([]any) {
			param := p.(map[string]any)
			if param["in"] == "path" {
				parameters = append(parameters, param["name"].(string))
			}
		}
		if !reflect.DeepEqual(parameters, []string{"id"}) {
			t.Errorf("%s: path parameters = %v, want [id]", tc.path, parameters)
		}
		body := jsonPath(operation, "requestBody", "content", "application/json", "schema", "$ref")
		if body != "#/components/schemas/"+tc.input {
			t.Errorf("%s: request body = %v, want %s", tc.path, body, tc.input)
		}
		responses := operation["responses"].(map[string]any)
		statuses := keys(responses)
		if want := []string{"200", "400", "403", "404", "409", "422", "500", "503"}; !reflect.DeepEqual(statuses, want) {
			t.Errorf("%s: responses = %v, want %v", tc.path, statuses, want)
		}
		if description, _ := jsonPath(responses, "409", "description").(string); description != "vetoed: a behavior's guard or handler refuses the change; details.vetoCode names the rule. conflict: a row the operation would write collides with one that exists." {
			t.Errorf("%s: 409 = %q", tc.path, description)
		}
	}
	if result := jsonPath(doc.Paths["/api/order/{id}/status/transition"]["post"], "responses", "200", "content", "application/json", "schema", "properties", "data", "$ref"); result != "#/components/schemas/OrderStatusTransitionResult" {
		t.Errorf("transition's result = %v", result)
	}
	if got := keys(doc.Paths["/api/orders/{id}"]["get"]["responses"].(map[string]any)); !reflect.DeepEqual(got, []string{"200", "400", "500"}) {
		t.Errorf("getOrder, no behavior operation, answers %v", got)
	}

	order := doc.Components.Schemas["Order"]
	properties := order["properties"].(map[string]any)
	for name, want := range map[string]bool{"status": true, "statusEnteredAt": true, "id": false, "title": false, "placedAt": false} {
		property, ok := properties[name].(map[string]any)
		if !ok {
			t.Errorf("Order has no property %s: %v", name, keys(properties))
			continue
		}
		if got := property["readOnly"] == true; got != want {
			t.Errorf("Order.%s readOnly = %v, want %v", name, got, want)
		}
	}
	input := doc.Components.Schemas["OrderStatusTransitionInput"]
	if to := jsonPath(input, "properties", "to"); to == nil || jsonPath(input, "properties", "to", "readOnly") != nil {
		t.Errorf("OrderStatusTransitionInput.to = %v", to)
	}
	if enum := doc.Components.Schemas["OrderStatusState"]["enum"]; !reflect.DeepEqual(enum, []any{"draft", "paid", "shipped"}) {
		t.Errorf("OrderStatusState = %v", doc.Components.Schemas["OrderStatusState"])
	}
}

// TestOpenAPIMarksAViewsOwnedFieldsReadOnly: fixture-workflow-view-api's
// @source view of a table whose fields are behaviors carries readOnly on
// the fields that project an owned field, by name, and on no other.
func TestOpenAPIMarksAViewsOwnedFieldsReadOnly(t *testing.T) {
	load := func(name string) *ir.Schema {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return schema
	}
	schema, db := load("fixture-workflow-view-api"), load("fixture-workflow-db")
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   "fixture-workflow-view-api",
		ModulePath:   "example.com/schemas/api/fixture-workflow-view-api",
		TypesModule:  "example.com/schemas/types/go/fixture-workflow-view-api",
		Dependencies: map[string]*ir.Schema{"fixture-workflow-db": db},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]map[string]any `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal([]byte(output.OpenAPISpecRaw), &doc); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"id": false, "title": false, "status": true, "statusEnteredAt": true} {
		if jsonPath(doc.Components.Schemas["OrderView"], "properties", name) == nil {
			t.Errorf("OrderView has no property %s", name)
		}
		if got := jsonPath(doc.Components.Schemas["OrderView"], "properties", name, "readOnly") == true; got != want {
			t.Errorf("OrderView.%s readOnly = %v, want %v", name, got, want)
		}
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func jsonPath(value any, path ...string) any {
	for _, key := range path {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = object[key]
	}
	return value
}
