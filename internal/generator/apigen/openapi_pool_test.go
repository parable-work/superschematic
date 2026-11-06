package apigen_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// TestOpenAPIPoolOperations: fixture-pool-api's OpenAPI document holds the
// operations of the Pool and Budget Account implements (D104) as any
// operation, under each use's member: markOut and reportRoom on a row, and
// pick, an operation of the table as a whole, at /api/account/pool/pick
// with no path parameter, its body AccountPoolPickInput, whose match and
// prefer are AccountPoolMatch, and its answer AccountPoolPickResult, whose
// member is AccountPoolPickMember or null; a member's availability is the
// enum AccountPoolAvailability; and every behavior operation declares the
// problems a refusal answers, pick's too, since a call a table operation
// makes on a row can be refused as any. The types of the event recheck and
// its payload, which only the behavior worker hands to the project's
// Workers method, reach no endpoint, so the document leaves them out.
// testdata/golden/fixture-pool-api/openapi.json is the document.
func TestOpenAPIPoolOperations(t *testing.T) {
	load := func(name string) *ir.Schema {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name), loader.WithWarnings(func(*verify.Diagnostic) {}))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return schema
	}
	schema, db := load("fixture-pool-api"), load("fixture-pool-db")
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   "fixture-pool-api",
		ModulePath:   "example.com/schemas/api/fixture-pool-api",
		TypesModule:  "example.com/schemas/types/go/fixture-pool-api",
		Dependencies: map[string]*ir.Schema{"fixture-pool-db": db},
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
	var markOutResponses []string
	for _, tc := range []struct{ path, operationID, input, result string }{
		{"/api/account/{id}/pool/mark-out", "AccountPoolMarkOutHandler", "AccountPoolMarkOutInput", "AccountPoolMarkOutResult"},
		{"/api/account/{id}/pool/report-room", "AccountPoolReportRoomHandler", "AccountPoolReportRoomInput", "AccountPoolReportRoomResult"},
		{"/api/account/pool/pick", "AccountPoolPickHandler", "AccountPoolPickInput", "AccountPoolPickResult"},
		{"/api/account/{id}/budget/observe", "AccountBudgetObserveHandler", "AccountBudgetObserveInput", "AccountBudgetObserveResult"},
	} {
		operation := doc.Paths[tc.path]["post"]
		if operation == nil {
			t.Errorf("no POST %s: %v", tc.path, keys(doc.Paths))
			continue
		}
		if operation["operationId"] != tc.operationID {
			t.Errorf("%s: operationId = %v, want %s", tc.path, operation["operationId"], tc.operationID)
		}
		if body := jsonPath(operation, "requestBody", "content", "application/json", "schema", "$ref"); body != "#/components/schemas/"+tc.input {
			t.Errorf("%s: request body = %v, want %s", tc.path, body, tc.input)
		}
		if result := jsonPath(operation, "responses", "200", "content", "application/json", "schema", "properties", "data", "$ref"); result != "#/components/schemas/"+tc.result {
			t.Errorf("%s: result = %v, want %s", tc.path, result, tc.result)
		}
		responses, _ := operation["responses"].(map[string]any)
		codes := keys(responses)
		switch tc.path {
		case "/api/account/{id}/pool/mark-out":
			markOutResponses = codes
		case "/api/account/pool/pick":
			if !slices.Equal(codes, markOutResponses) {
				t.Errorf("pick's responses = %v, want a row operation's, %v", codes, markOutResponses)
			}
			if parameters := operation["parameters"]; parameters != nil {
				t.Errorf("pick's parameters = %v, want none: it names no row", parameters)
			}
		}
	}
	if _, ok := doc.Paths["/api/account/{id}/pool/pick"]; ok {
		t.Errorf("the document serves pick on a row: it runs on the table as a whole")
	}
	if enum := doc.Components.Schemas["AccountPoolAvailability"]["enum"]; !reflect.DeepEqual(enum, []any{"open", "near", "out"}) {
		t.Errorf("AccountPoolAvailability = %v", doc.Components.Schemas["AccountPoolAvailability"])
	}
	for _, at := range []struct{ schema, property, ref string }{
		{"AccountPoolPickInput", "match", "AccountPoolMatch"},
		{"AccountPoolPickInput", "prefer", "AccountPoolMatch"},
		{"AccountPoolPickResult", "member", "AccountPoolPickMember"},
		{"AccountPoolPickMember", "availability", "AccountPoolAvailability"},
		{"AccountPoolMarkOutResult", "availability", "AccountPoolAvailability"},
		{"AccountPoolReportRoomResult", "availability", "AccountPoolAvailability"},
	} {
		if ref := refOf(jsonPath(doc.Components.Schemas[at.schema], "properties", at.property)); ref != "#/components/schemas/"+at.ref {
			t.Errorf("%s.%s = %v, want %s", at.schema, at.property, ref, at.ref)
		}
	}
	if properties, _ := jsonPath(doc.Components.Schemas["AccountPoolMatch"], "properties").(map[string]any); !slices.Equal(keys(properties), []string{"kind", "name", "owner", "rank", "vendor"}) {
		t.Errorf("AccountPoolMatch = %v, want kind, name, owner, rank and vendor", properties)
	}
	for _, name := range []string{"AccountPoolRecheckEvent", "AccountPoolRecheckPayload"} {
		if doc.Components.Schemas[name] != nil {
			t.Errorf("the document holds %s, which no endpoint references", name)
		}
	}
}
