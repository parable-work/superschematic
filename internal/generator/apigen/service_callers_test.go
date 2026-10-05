package apigen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

const serviceAuthAPI = "fixture-service-auth-api"

func generateServiceAuthFixtureAPI(t *testing.T) *apigen.APIOutput {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, serviceAuthAPI))
	if err != nil {
		t.Fatalf("load %s: %v", serviceAuthAPI, err)
	}
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  serviceAuthAPI,
		ModulePath:  "example.com/schemas/api/" + serviceAuthAPI,
		TypesModule: "example.com/schemas/types/go/" + serviceAuthAPI,
		Clock:       codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return output
}

// TestEndpointServiceCallersAreEffective: each endpoint carries its
// operation's effective service clause, its own or else its set's, none on
// an @publicRoute operation; RequiresAuth stays the user clause alone; and
// ServiceOnly marks the @requireService endpoints.
func TestEndpointServiceCallersAreEffective(t *testing.T) {
	output := generateServiceAuthFixtureAPI(t)
	if !output.HasServiceCallers {
		t.Error("HasServiceCallers = false, want true")
	}
	caller := []string{"fixture-service-caller-api"}
	type want struct {
		clause       *ir.ServiceCallers
		requiresAuth bool
		serviceOnly  bool
	}
	require := func(from []string) *ir.ServiceCallers {
		return &ir.ServiceCallers{Mode: ir.ServiceCallersRequire, From: from}
	}
	allow := func(from []string) *ir.ServiceCallers {
		return &ir.ServiceCallers{Mode: ir.ServiceCallersAllow, From: from}
	}
	wants := map[string]want{
		"reserveStock":       {require(caller), true, true},
		"releaseReservation": {allow(caller), true, false},
		"reindexStock":       {require(nil), false, true},
		"getReservation":     {nil, true, false},
		"syncStock":          {require(caller), false, true},
		"syncMyStock":        {allow(nil), true, false},
		"syncStatus":         {nil, false, false},
		"listReservations":   {allow(caller), true, false},
	}
	if len(output.Endpoints) != len(wants) {
		t.Fatalf("got %d endpoints, want %d", len(output.Endpoints), len(wants))
	}
	for _, endpoint := range output.Endpoints {
		w, ok := wants[endpoint.Name]
		if !ok {
			t.Errorf("unexpected endpoint %s", endpoint.Name)
			continue
		}
		if !reflect.DeepEqual(endpoint.ServiceCallers, w.clause) {
			t.Errorf("%s ServiceCallers = %+v, want %+v", endpoint.Name, endpoint.ServiceCallers, w.clause)
		}
		if endpoint.RequiresAuth != w.requiresAuth {
			t.Errorf("%s RequiresAuth = %v, want %v", endpoint.Name, endpoint.RequiresAuth, w.requiresAuth)
		}
		if endpoint.ServiceOnly() != w.serviceOnly {
			t.Errorf("%s ServiceOnly = %v, want %v", endpoint.Name, endpoint.ServiceOnly(), w.serviceOnly)
		}
	}
}

// TestOpenAPIServiceCallersSecurity: the document declares serviceAuth on
// Service-Authorization and writes each rule as a security list, an OR of
// ANDs, with the from list under x-service-callers.
func TestOpenAPIServiceCallersSecurity(t *testing.T) {
	output := generateServiceAuthFixtureAPI(t)
	var spec map[string]any
	if err := json.Unmarshal([]byte(output.OpenAPISpecRaw), &spec); err != nil {
		t.Fatal(err)
	}
	schemes := spec["components"].(map[string]any)["securitySchemes"].(map[string]any)
	scheme, ok := schemes["serviceAuth"].(map[string]any)
	if !ok {
		t.Fatalf("no serviceAuth scheme in %v", schemes)
	}
	if scheme["type"] != "apiKey" || scheme["in"] != "header" || scheme["name"] != "Service-Authorization" ||
		!strings.Contains(scheme["description"].(string), "Bearer <jwt>") {
		t.Errorf("serviceAuth = %v", scheme)
	}

	user := map[string]any{"bearerAuth": []any{}}
	service := map[string]any{"serviceAuth": []any{}}
	both := map[string]any{"serviceAuth": []any{}, "bearerAuth": []any{}}
	callers := []any{"fixture-service-caller-api"}
	for _, tc := range []struct {
		path, method string
		security     []any
		callers      []any
	}{
		{"/api/stock/reservations", "post", []any{both}, callers},
		{"/api/stock/reservations/{id}/release", "post", []any{user, service}, callers},
		{"/api/stock/reindex", "post", []any{service}, nil},
		{"/api/stock/reservations/{id}", "get", []any{user}, nil},
		{"/api/sync/stock", "post", []any{service}, callers},
		{"/api/sync/stock/mine", "post", []any{user, service}, nil},
		{"/api/sync/status", "get", nil, nil},
		{"/api/ledger/reservations", "get", []any{user, service}, callers},
	} {
		op := openAPIOperation(t, spec, tc.path, tc.method)
		security, _ := op["security"].([]any)
		if !reflect.DeepEqual(security, tc.security) {
			t.Errorf("%s %s security = %v, want %v", tc.method, tc.path, op["security"], tc.security)
		}
		got, _ := op[apigen.OpenAPIServiceCallersKey].([]any)
		if !reflect.DeepEqual(got, tc.callers) {
			t.Errorf("%s %s %s = %v, want %v", tc.method, tc.path, apigen.OpenAPIServiceCallersKey, op[apigen.OpenAPIServiceCallersKey], tc.callers)
		}
	}
}

// TestOpenAPIWithoutServiceCallersHasNoServiceAuth: a schema without a
// service clause keeps its document as it was, with bearerAuth alone.
func TestOpenAPIWithoutServiceCallersHasNoServiceAuth(t *testing.T) {
	output := generateDocsFixtureAPI(t)
	if output.HasServiceCallers {
		t.Error("HasServiceCallers = true for a schema without a service clause")
	}
	if strings.Contains(output.OpenAPISpecRaw, "serviceAuth") || strings.Contains(output.OpenAPISpecRaw, apigen.OpenAPIServiceCallersKey) {
		t.Error("a schema without a service clause declares serviceAuth")
	}
}

// TestOpenAPIServiceCallersGolden pins the OpenAPI document of
// fixture-service-auth-api. Regenerate with
// go test ./internal/generator/apigen -run TestOpenAPIServiceCallersGolden -update
func TestOpenAPIServiceCallersGolden(t *testing.T) {
	output := generateServiceAuthFixtureAPI(t)
	golden := filepath.Join("testdata", "golden", serviceAuthAPI, "openapi.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(output.OpenAPISpecRaw), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("missing golden (run with -update): %v", err)
	}
	if string(want) != output.OpenAPISpecRaw {
		t.Fatalf("OpenAPI document changed; run with -update and review the diff\ngot:\n%s", output.OpenAPISpecRaw)
	}
}
