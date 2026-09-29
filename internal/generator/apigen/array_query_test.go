package apigen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
)

// TestGeneratedArrayQueryParamsPreserveWireAndValidation: fixture-api's
// listTenants takes two array query parameters. The implementation receives
// slices (nil when an optional one is absent), the handler decodes each with
// bodyargs.QueryList, as a list argument of a GET operation, which reads
// each comma-separated item as its JSON type and checks it at name[i] with
// the list bounds, and the OpenAPI parameter is an array with
// form/explode=false and the list bounds.
func TestGeneratedArrayQueryParamsPreserveWireAndValidation(t *testing.T) {
	output := generateFixtureAPI(t)
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}

	interfaces, err := os.ReadFile(filepath.Join(outDir, "interfaces.go"))
	if err != nil {
		t.Fatalf("read generated interfaces: %v", err)
	}
	interfaceSource := string(interfaces)
	if !strings.Contains(interfaceSource, "ids []types.IdentityUUID, statuses []types.TenantListStatus") {
		t.Fatalf("array query params must stay slices in the implementation interface:\n%s", interfaceSource)
	}
	if strings.Contains(interfaceSource, "statuses *[]types.TenantListStatus") {
		t.Fatal("an optional array query param uses a nil slice for absence, not a pointer to a slice")
	}

	routes, err := os.ReadFile(filepath.Join(outDir, "routes.go"))
	if err != nil {
		t.Fatalf("read generated routes: %v", err)
	}
	routeSource := string(routes)
	for _, want := range []string{
		`queryListIdsArg := bodyargs.NewArg("ids", bodyargs.String, bodyargs.Required(), bodyargs.ListMin(1), bodyargs.ListMax(100)`,
		`queryListStatusesArg := bodyargs.NewArg("statuses", bodyargs.String, bodyargs.ListMin(0), bodyargs.ListMax(10))`,
		`Ids = bodyargs.QueryList[types.IdentityUUID](listErrors, r.URL.Query(), queryListIdsArg)`,
		`Statuses = bodyargs.QueryList[types.TenantListStatus](listErrors, r.URL.Query(), queryListStatusesArg)`,
	} {
		if !strings.Contains(routeSource, want) {
			t.Errorf("generated array query parser missing %q", want)
		}
	}
	if strings.Contains(routeSource, "parseArrayQueryParam") {
		t.Error("routes.go still carries parseArrayQueryParam")
	}

	var spec map[string]any
	if err := json.Unmarshal([]byte(output.OpenAPISpecRaw), &spec); err != nil {
		t.Fatalf("decode OpenAPI: %v", err)
	}
	operation := spec["paths"].(map[string]any)["/api/tenants"].(map[string]any)["get"].(map[string]any)
	parameters := operation["parameters"].([]any)
	for _, name := range []string{"ids", "statuses"} {
		var found map[string]any
		for _, raw := range parameters {
			candidate := raw.(map[string]any)
			if candidate["name"] == name {
				found = candidate
				break
			}
		}
		if found == nil {
			t.Fatalf("OpenAPI query parameter %q not found", name)
		}
		if found["style"] != "form" || found["explode"] != false {
			t.Errorf("OpenAPI query parameter %q wire metadata = %#v", name, found)
		}
		schema := found["schema"].(map[string]any)
		if schema["type"] != "array" || schema["items"] == nil {
			t.Errorf("OpenAPI query parameter %q schema = %#v, want array items", name, schema)
		}
	}
}
