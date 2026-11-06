package apigen_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// TestOpenAPISchedulesOperations: fixture-schedules-api's OpenAPI document
// holds the operations of the Schedules Problem implements (D104) as any
// operation, under the use's member, beside its Workflow transition:
// listSchedules a POST that reads, as every behavior operation is; every
// kind the enum of every kind, a schedule's state and what finished it
// enums of their own; reportRun's input's outcome the enum of the config's
// outcomes, and an answer's outcome and lastOutcome strings, so a reader
// takes one a later config dropped; and the permission catalog lists
// problems.schedule under each of Schedules' operations. The types of the
// event run and its payload, which only the behavior worker hands to the
// project's Workers method, reach no endpoint, so the document leaves them
// out. testdata/golden/fixture-schedules-api/openapi.json is the document.
func TestOpenAPISchedulesOperations(t *testing.T) {
	load := func(name string) *ir.Schema {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name), loader.WithWarnings(func(*verify.Diagnostic) {}))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return schema
	}
	schema, db := load("fixture-schedules-api"), load("fixture-schedules-db")
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   "fixture-schedules-api",
		ModulePath:   "example.com/schemas/api/fixture-schedules-api",
		TypesModule:  "example.com/schemas/types/go/fixture-schedules-api",
		Dependencies: map[string]*ir.Schema{"fixture-schedules-db": db},
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
	var handlers []string
	for _, tc := range []struct{ path, operationID, input, result string }{
		{"/api/problem/{id}/schedules/set-schedule", "ProblemSchedulesSetScheduleHandler", "ProblemSchedulesSetScheduleInput", "ProblemSchedulesSetScheduleResult"},
		{"/api/problem/{id}/schedules/pause-schedule", "ProblemSchedulesPauseScheduleHandler", "ProblemSchedulesPauseScheduleInput", "ProblemSchedulesPauseScheduleResult"},
		{"/api/problem/{id}/schedules/resume-schedule", "ProblemSchedulesResumeScheduleHandler", "ProblemSchedulesResumeScheduleInput", "ProblemSchedulesResumeScheduleResult"},
		{"/api/problem/{id}/schedules/cancel-schedule", "ProblemSchedulesCancelScheduleHandler", "ProblemSchedulesCancelScheduleInput", "ProblemSchedulesCancelScheduleResult"},
		{"/api/problem/{id}/schedules/report-run", "ProblemSchedulesReportRunHandler", "ProblemSchedulesReportRunInput", "ProblemSchedulesReportRunResult"},
		{"/api/problem/{id}/schedules/list-schedules", "ProblemSchedulesListSchedulesHandler", "ProblemSchedulesListSchedulesInput", "ProblemSchedulesListSchedulesResult"},
		{"/api/problem/{id}/status/transition", "ProblemStatusTransitionHandler", "ProblemStatusTransitionInput", "ProblemStatusTransitionResult"},
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
		if strings.HasPrefix(tc.operationID, "ProblemSchedules") {
			handlers = append(handlers, tc.operationID)
		}
	}
	for name, want := range map[string][]any{
		"ProblemSchedulesKind":    {"after", "every", "cron"},
		"ProblemSchedulesOutcome": {"told", "resumed", "held", "missed"},
		"ProblemSchedulesState":   {"active", "paused", "finished"},
		"ProblemSchedulesEndedBy": {"run", "cancel", "endOn"},
	} {
		if enum := doc.Components.Schemas[name]["enum"]; !reflect.DeepEqual(enum, want) {
			t.Errorf("%s = %v, want %v", name, doc.Components.Schemas[name], want)
		}
	}
	for _, at := range []struct {
		schema, property, ref string
	}{
		{"ProblemSchedulesSetScheduleResult", "kind", "ProblemSchedulesKind"},
		{"ProblemSchedulesSetScheduleResult", "state", "ProblemSchedulesState"},
		{"ProblemSchedulesSetScheduleResult", "endedBy", "ProblemSchedulesEndedBy"},
		{"ProblemSchedulesListSchedulesResultItems", "kind", "ProblemSchedulesKind"},
		{"ProblemSchedulesReportRunInput", "outcome", "ProblemSchedulesOutcome"},
	} {
		if ref := refOf(jsonPath(doc.Components.Schemas[at.schema], "properties", at.property)); ref != "#/components/schemas/"+at.ref {
			t.Errorf("%s.%s = %v, want %s", at.schema, at.property, ref, at.ref)
		}
	}
	// What a call answers keeps any outcome a report recorded, a later
	// config's dropped one among them: a string, not the input's enum.
	for _, at := range []struct{ schema, property string }{
		{"ProblemSchedulesSetScheduleResult", "lastOutcome"},
		{"ProblemSchedulesListSchedulesResultItems", "lastOutcome"},
		{"ProblemSchedulesReportRunResult", "outcome"},
	} {
		if kind := jsonPath(doc.Components.Schemas[at.schema], "properties", at.property, "type"); kind != "string" {
			t.Errorf("%s.%s = %v, want a string", at.schema, at.property, jsonPath(doc.Components.Schemas[at.schema], "properties", at.property))
		}
	}
	if properties, _ := jsonPath(doc.Components.Schemas["ProblemSchedulesSetScheduleInput"], "properties").(map[string]any); !slices.Equal(keys(properties), []string{"after", "cron", "every", "name", "timeZone"}) {
		t.Errorf("ProblemSchedulesSetScheduleInput = %v, want after, cron, every, name and timeZone", properties)
	}
	for _, name := range []string{"ProblemSchedulesRunEvent", "ProblemSchedulesRunPayload"} {
		if doc.Components.Schemas[name] != nil {
			t.Errorf("the document holds %s, which no endpoint references", name)
		}
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
	slices.Sort(handlers)
	if len(catalog.Permissions) != 1 || catalog.Permissions[0].Name != "problems.schedule" || !slices.Equal(catalog.Permissions[0].Operations, handlers) {
		t.Errorf("the catalog = %+v, want problems.schedule under %v", catalog.Permissions, handlers)
	}
}
