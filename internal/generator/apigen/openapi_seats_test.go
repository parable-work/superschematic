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

// TestOpenAPISeatsOperations: fixture-seats-api's OpenAPI document holds the
// operations of the Seats Problem implements and of the Reconcile on
// Sandbox's observed field (D104) as any operation, under each use's
// member, beside each table's Workflow transition: listSeats a POST that
// reads, as every behavior operation is; every seat the enum of the
// config's seats and every observed state the enum of the Workflow's
// states its status names; listSeats' seatedAt and seatedBy, and observe's
// status, nullable; no request body presents preconditions, since no use
// takes one; and the permission catalog lists Seats' take and assign
// permissions under each of its operations, and Reconcile's
// observePermission under observe. testdata/golden/fixture-seats-api/openapi.json,
// which TestWriteAPIGoldenSeats holds, is the document.
func TestOpenAPISeatsOperations(t *testing.T) {
	load := func(name string) *ir.Schema {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name), loader.WithWarnings(func(*verify.Diagnostic) {}))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return schema
	}
	schema, db := load("fixture-seats-api"), load("fixture-seats-db")
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   "fixture-seats-api",
		ModulePath:   "example.com/schemas/api/fixture-seats-api",
		TypesModule:  "example.com/schemas/types/go/fixture-seats-api",
		Dependencies: map[string]*ir.Schema{"fixture-seats-db": db},
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
	for _, tc := range []struct{ path, operationID, input, result string }{
		{"/api/problem/{id}/seats/take-seat", "ProblemSeatsTakeSeatHandler", "ProblemSeatsTakeSeatInput", "ProblemSeatsTakeSeatResult"},
		{"/api/problem/{id}/seats/leave-seat", "ProblemSeatsLeaveSeatHandler", "ProblemSeatsLeaveSeatInput", "ProblemSeatsLeaveSeatResult"},
		{"/api/problem/{id}/seats/list-seats", "ProblemSeatsListSeatsHandler", "ProblemSeatsListSeatsInput", "ProblemSeatsListSeatsResult"},
		{"/api/problem/{id}/status/transition", "ProblemStatusTransitionHandler", "ProblemStatusTransitionInput", "ProblemStatusTransitionResult"},
		{"/api/sandbox/{id}/status/transition", "SandboxStatusTransitionHandler", "SandboxStatusTransitionInput", "SandboxStatusTransitionResult"},
		{"/api/sandbox/{id}/observed/observe", "SandboxObservedObserveHandler", "SandboxObservedObserveInput", "SandboxObservedObserveResult"},
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
		if ref := refOf(jsonPath(doc.Components.Schemas[tc.input], "properties", "preconditions")); ref != nil {
			t.Errorf("%s.preconditions = %v, want none: no use of its table takes a precondition", tc.input, ref)
		}
	}
	for name, want := range map[string][]any{
		"ProblemSeatsSeat":     {"accountable", "handles", "responsible"},
		"SandboxObservedState": {"pending", "starting", "running", "stopping", "stopped", "failed", "unreachable"},
	} {
		if enum := doc.Components.Schemas[name]["enum"]; !reflect.DeepEqual(enum, want) {
			t.Errorf("%s = %v, want %v", name, doc.Components.Schemas[name], want)
		}
	}
	for _, at := range []struct {
		schema, property, ref string
	}{
		{"ProblemSeatsTakeSeatInput", "seat", "ProblemSeatsSeat"},
		{"ProblemSeatsLeaveSeatResult", "seat", "ProblemSeatsSeat"},
		{"ProblemSeatsListSeatsResultItems", "seat", "ProblemSeatsSeat"},
		{"SandboxObservedObserveInput", "state", "SandboxObservedState"},
		{"SandboxObservedObserveResult", "observed", "SandboxObservedState"},
		{"SandboxObservedObserveResult", "status", "SandboxObservedState"},
		{"SandboxObservedObserveResultMoves", "from", "SandboxObservedState"},
	} {
		if ref := refOf(jsonPath(doc.Components.Schemas[at.schema], "properties", at.property)); ref != "#/components/schemas/"+at.ref {
			t.Errorf("%s.%s = %v, want %s", at.schema, at.property, ref, at.ref)
		}
	}
	items := doc.Components.Schemas["ProblemSeatsListSeatsResultItems"]
	for _, property := range []string{"seatedAt", "seatedBy"} {
		if nullable := jsonPath(items, "properties", property, "nullable"); nullable != true {
			t.Errorf("ProblemSeatsListSeatsResultItems.%s = %v, want it nullable", property, jsonPath(items, "properties", property))
		}
	}
	if status := jsonPath(doc.Components.Schemas["SandboxObservedObserveResult"], "properties", "status"); !reflect.DeepEqual(jsonPath(status, "oneOf"), []any{
		map[string]any{"$ref": "#/components/schemas/SandboxObservedState"}, map[string]any{"enum": []any{nil}, "nullable": true, "type": "string"},
	}) {
		t.Errorf("SandboxObservedObserveResult.status = %v, want the state or null", status)
	}

	var catalog struct {
		Permissions []struct {
			Name       string   `json:"name"`
			Operations []string `json:"operations"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(output.PermissionCatalogJSON), &catalog); err != nil {
		t.Fatalf("permissions.json: %v\n%s", err, output.PermissionCatalogJSON)
	}
	operations := map[string][]string{}
	for _, p := range catalog.Permissions {
		operations[p.Name] = p.Operations
	}
	seatsOperations := []string{"ProblemSeatsLeaveSeatHandler", "ProblemSeatsListSeatsHandler", "ProblemSeatsTakeSeatHandler"}
	for permission, want := range map[string][]string{
		"problems.assign": seatsOperations, "problems.own": seatsOperations, "problems.release": seatsOperations, "problems.work": seatsOperations,
		"problems.override": {"ProblemStatusTransitionHandler"}, "sandboxes.observe": {"SandboxObservedObserveHandler"}, "sandboxes.stop": {"SandboxStatusTransitionHandler"},
	} {
		if got := operations[permission]; !slices.Equal(got, want) {
			t.Errorf("%s is required by %v, want %v", permission, got, want)
		}
	}
}
