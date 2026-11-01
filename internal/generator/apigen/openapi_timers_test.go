package apigen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestOpenAPITimersOperations: fixture-timers-api's OpenAPI document holds
// the operations of the Timers its Ticket implements (D103) as any
// operation, under the use's member, timers: each at its route under the
// table's key, with its input and result types, the enum of the config's
// deadlines and the enum of a fire's outcomes, and the problems the
// behaviors' core answers; and the document is held to its golden,
// testdata/golden/fixture-timers-api/openapi.json, and its permission
// catalog to permissions.json.
func TestOpenAPITimersOperations(t *testing.T) {
	load := func(name string) *ir.Schema {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return schema
	}
	schema, db := load("fixture-timers-api"), load("fixture-timers-db")
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   "fixture-timers-api",
		ModulePath:   "example.com/schemas/api/fixture-timers-api",
		TypesModule:  "example.com/schemas/types/go/fixture-timers-api",
		Dependencies: map[string]*ir.Schema{"fixture-timers-db": db},
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
		{"/api/ticket/{id}/timers/fire-timer", "TicketTimersFireTimerHandler", "TicketTimersFireTimerInput", "TicketTimersFireTimerResult"},
		{"/api/ticket/{id}/timers/reset-timer", "TicketTimersResetTimerHandler", "TicketTimersResetTimerInput", "TicketTimersResetTimerResult"},
		{"/api/ticket/{id}/timers/snooze-timer", "TicketTimersSnoozeTimerHandler", "TicketTimersSnoozeTimerInput", "TicketTimersSnoozeTimerResult"},
		{"/api/ticket/{id}/status/transition", "TicketStatusTransitionHandler", "TicketStatusTransitionInput", "TicketStatusTransitionResult"},
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
		if got, want := keys(operation["responses"].(map[string]any)), []string{"200", "400", "401", "403", "404", "409", "422", "500", "503"}; !reflect.DeepEqual(got, want) && !reflect.DeepEqual(got, []string{"200", "400", "403", "404", "409", "422", "500", "503"}) {
			t.Errorf("%s: responses = %v", tc.path, got)
		}
	}
	if enum := doc.Components.Schemas["TicketTimersDeadline"]["enum"]; !reflect.DeepEqual(enum, []any{"idle", "remind", "respond"}) {
		t.Errorf("TicketTimersDeadline = %v", doc.Components.Schemas["TicketTimersDeadline"])
	}
	if enum := doc.Components.Schemas["TicketTimersFireTimerResultOutcome"]["enum"]; !reflect.DeepEqual(enum, []any{"moved", "skipped", "vetoed", "forbidden"}) {
		t.Errorf("TicketTimersFireTimerResultOutcome = %v", doc.Components.Schemas["TicketTimersFireTimerResultOutcome"])
	}
	if ref := jsonPath(doc.Components.Schemas["TicketTimersFireTimerInput"], "properties", "deadline", "$ref"); ref != "#/components/schemas/TicketTimersDeadline" {
		t.Errorf("TicketTimersFireTimerInput.deadline = %v", ref)
	}
	if minimum := jsonPath(doc.Components.Schemas["TicketTimersSnoozeTimerInput"], "properties", "dueAt", "minimum"); minimum != float64(0) {
		t.Errorf("TicketTimersSnoozeTimerInput.dueAt's minimum = %v, want 0", minimum)
	}

	// The permission catalog lists the route's tickets.write and the
	// config's tickets.timers under each Timers operation, which the core
	// checks a reset's and a snooze's caller against.
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
	timers := []string{"TicketTimersFireTimerHandler", "TicketTimersResetTimerHandler", "TicketTimersSnoozeTimerHandler"}
	if got := operations["tickets.timers"]; !reflect.DeepEqual(got, timers) {
		t.Errorf("tickets.timers is required by %v, want %v", got, timers)
	}
	if got := operations["tickets.write"]; len(got) != 4 {
		t.Errorf("tickets.write is required by %v, want the four operations", got)
	}

	for name, got := range map[string]string{"openapi.json": output.OpenAPISpecRaw, "permissions.json": output.PermissionCatalogJSON} {
		golden := filepath.Join("testdata", "golden", "fixture-timers-api", name)
		if *update {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("missing golden (run with -update): %v", err)
		}
		if got != string(want) {
			t.Errorf("%s differs from %s (run with -update to accept)", name, golden)
		}
	}
}
