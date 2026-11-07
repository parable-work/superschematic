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

// TestOpenAPIAgentsOperations: fixture-agents-api's OpenAPI document holds
// the operations of the Lease, Retries, Presence and Budget its tables
// implement (D104) as any operation, under each use's member, Lease's
// expireHolder, an operation of the table as a whole, at
// /api/job/lease/expire-holder with no path parameter; every Job row
// operation's request body presents the member preconditions,
// JobPreconditions, whose member Lease is JobLeasePrecondition, a token of
// at least 0, and expireHolder's and no other table's does;
// recordAttempt's failure is the enum of the config's classes
// and every Budget operation's meter the enum of its meters; an operation
// of no parameter and no precondition (Session's beat) takes no body; and
// the permission catalog lists each config permission under each of its
// use's operations. testdata/golden/fixture-agents-api/openapi.json, which
// TestWriteAPIGoldenAgents holds, is the document.
func TestOpenAPIAgentsOperations(t *testing.T) {
	load := func(name string) *ir.Schema {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name), loader.WithWarnings(func(*verify.Diagnostic) {}))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return schema
	}
	schema, db := load("fixture-agents-api"), load("fixture-agents-db")
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   "fixture-agents-api",
		ModulePath:   "example.com/schemas/api/fixture-agents-api",
		TypesModule:  "example.com/schemas/types/go/fixture-agents-api",
		Dependencies: map[string]*ir.Schema{"fixture-agents-db": db},
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
		{"/api/job/{id}/lease/acquire", "JobLeaseAcquireHandler", "JobLeaseAcquireInput", "JobLeaseAcquireResult"},
		{"/api/job/{id}/lease/heartbeat", "JobLeaseHeartbeatHandler", "JobLeaseHeartbeatInput", "JobLeaseHeartbeatResult"},
		{"/api/job/{id}/lease/reset-expiries", "JobLeaseResetExpiriesHandler", "JobLeaseResetExpiriesInput", "JobLeaseResetExpiriesResult"},
		{"/api/job/lease/expire-holder", "JobLeaseExpireHolderHandler", "JobLeaseExpireHolderInput", "JobLeaseExpireHolderResult"},
		{"/api/job/{id}/retries/record-attempt", "JobRetriesRecordAttemptHandler", "JobRetriesRecordAttemptInput", "JobRetriesRecordAttemptResult"},
		{"/api/job/{id}/status/transition", "JobStatusTransitionHandler", "JobStatusTransitionInput", "JobStatusTransitionResult"},
		{"/api/session/{id}/presence/beat", "SessionPresenceBeatHandler", "", "SessionPresenceBeatResult"},
		{"/api/account/{id}/budget/observe", "AccountBudgetObserveHandler", "AccountBudgetObserveInput", "AccountBudgetObserveResult"},
		{"/api/account/{id}/budget/record-usage-for", "AccountBudgetRecordUsageForHandler", "AccountBudgetRecordUsageForInput", "AccountBudgetRecordUsageForResult"},
	} {
		operation := doc.Paths[tc.path]["post"]
		if operation == nil {
			t.Errorf("no POST %s: %v", tc.path, keys(doc.Paths))
			continue
		}
		if operation["operationId"] != tc.operationID {
			t.Errorf("%s: operationId = %v, want %s", tc.path, operation["operationId"], tc.operationID)
		}
		body := jsonPath(operation, "requestBody", "content", "application/json", "schema", "$ref")
		switch {
		case tc.input == "" && operation["requestBody"] != nil:
			t.Errorf("%s: request body = %v, want none", tc.path, operation["requestBody"])
		case tc.input != "" && body != "#/components/schemas/"+tc.input:
			t.Errorf("%s: request body = %v, want %s", tc.path, body, tc.input)
		}
		if result := jsonPath(operation, "responses", "200", "content", "application/json", "schema", "properties", "data", "$ref"); result != "#/components/schemas/"+tc.result {
			t.Errorf("%s: result = %v, want %s", tc.path, result, tc.result)
		}
	}
	if _, ok := doc.Paths["/api/job/{id}/lease/expire-holder"]; ok {
		t.Errorf("the document serves expireHolder on a row: it runs on the table as a whole")
	}
	if parameters := jsonPath(doc.Paths["/api/job/lease/expire-holder"]["post"], "parameters"); parameters != nil {
		t.Errorf("/api/job/lease/expire-holder: parameters = %v, want none: it names no row", parameters)
	}
	if ref := refOf(jsonPath(doc.Components.Schemas["JobLeaseExpireHolderResult"], "properties", "reasons")); ref != "#/components/schemas/JobLeaseExpireHolderReasons" {
		t.Errorf("JobLeaseExpireHolderResult.reasons = %v, want JobLeaseExpireHolderReasons", ref)
	}
	for name, input := range doc.Components.Schemas {
		if !slices.Contains([]string{"Input"}, name[max(0, len(name)-5):]) {
			continue
		}
		ref := refOf(jsonPath(input, "properties", "preconditions"))
		// A precondition is a row's: an operation of the table as a whole
		// presents none (D104).
		job := len(name) > 3 && name[:3] == "Job" && name != "JobLeaseExpireHolderInput"
		switch {
		case job && ref != "#/components/schemas/JobPreconditions":
			t.Errorf("%s.preconditions = %v, want JobPreconditions", name, ref)
		case !job && ref != nil:
			t.Errorf("%s.preconditions = %v, want none: no use of its table takes a precondition", name, ref)
		}
	}
	if ref := refOf(jsonPath(doc.Components.Schemas["JobPreconditions"], "properties", "Lease")); ref != "#/components/schemas/JobLeasePrecondition" {
		t.Errorf("JobPreconditions.Lease = %v, want JobLeasePrecondition", doc.Components.Schemas["JobPreconditions"])
	}
	precondition := doc.Components.Schemas["JobLeasePrecondition"]
	if minimum := jsonPath(precondition, "properties", "token", "minimum"); minimum != float64(0) || !reflect.DeepEqual(precondition["required"], []any{"token"}) {
		t.Errorf("JobLeasePrecondition = %v, want a required token of at least 0", precondition)
	}
	if enum := doc.Components.Schemas["JobRetriesClass"]["enum"]; !reflect.DeepEqual(enum, []any{"build", "rejected", "timeout"}) {
		t.Errorf("JobRetriesClass = %v", doc.Components.Schemas["JobRetriesClass"])
	}
	if ref := refOf(jsonPath(doc.Components.Schemas["JobRetriesRecordAttemptInput"], "properties", "failure")); ref != "#/components/schemas/JobRetriesClass" {
		t.Errorf("JobRetriesRecordAttemptInput.failure = %v", ref)
	}
	if enum := doc.Components.Schemas["AccountBudgetMeter"]["enum"]; !reflect.DeepEqual(enum, []any{"questions", "tokens"}) {
		t.Errorf("AccountBudgetMeter = %v", doc.Components.Schemas["AccountBudgetMeter"])
	}
	if ref := refOf(jsonPath(doc.Components.Schemas["AccountBudgetObserveInput"], "properties", "meter")); ref != "#/components/schemas/AccountBudgetMeter" {
		t.Errorf("AccountBudgetObserveInput.meter = %v", ref)
	}

	// The permission catalog lists Job's Lease's jobs.override under each of
	// Lease's operations, expireHolder's among them, and Account's
	// accounts.limits and accounts.observe under each of Budget's.
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
	for permission, want := range map[string]int{"jobs.override": 8, "accounts.limits": 9, "accounts.observe": 9} {
		if got := operations[permission]; len(got) != want {
			t.Errorf("%s is required by %v, want %d operations", permission, got, want)
		}
	}
}

// refOf is the $ref a property's schema names: its own, or, for an
// optional one the document writes as oneOf the reference and null, the
// first of its oneOf's; nil for none.
func refOf(node any) any {
	if ref := jsonPath(node, "$ref"); ref != nil {
		return ref
	}
	if options, ok := jsonPath(node, "oneOf").([]any); ok && len(options) > 0 {
		return jsonPath(options[0], "$ref")
	}
	return nil
}
