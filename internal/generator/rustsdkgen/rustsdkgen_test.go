package rustsdkgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
)

const fixturesDir = "../../loader/tsreader/testdata/services"

func loadFixtureAPI(t *testing.T) *apigen.APIOutput {
	t.Helper()

	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}

	output, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  "fixture-api",
		ModulePath:  "example.com/schemas/api/fixture-api",
		TypesModule: "example.com/schemas/types/go/fixture-api",
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return output
}

func TestConvertEndpointBindsQueryEmbeddedPathParams(t *testing.T) {
	ep := apigen.EndpointInfo{
		Name:   "completeConnectorResearch",
		Path:   "/api/connector-requests/{requestId}/research/{researchId}/actions/complete",
		Method: "POST",
		QueryParams: []apigen.Param{
			{Name: "requestId", Type: "Identity.UUID", Required: true},
			{Name: "researchId", Type: "Identity.UUID", Required: false},
		},
	}

	converted := convertEndpoint(ep, false, "")

	if converted.PathFormat != "/api/connector-requests/{}/research/{}/actions/complete" {
		t.Fatalf("unexpected path format: %s", converted.PathFormat)
	}
	if len(converted.PathArgs) != 2 || converted.PathArgs[0] != "request_id" || converted.PathArgs[1] != "research_id" {
		t.Fatalf("unexpected path args: %v", converted.PathArgs)
	}
	if len(converted.PathQueryBindings) != 2 {
		t.Fatalf("expected 2 query bindings, got %v", converted.PathQueryBindings)
	}
	if converted.PathQueryBindings[0].RustName != "request_id" || !converted.PathQueryBindings[0].Required {
		t.Fatalf("unexpected first binding: %+v", converted.PathQueryBindings[0])
	}
	if converted.PathQueryBindings[1].RustName != "research_id" || converted.PathQueryBindings[1].Required {
		t.Fatalf("unexpected second binding: %+v", converted.PathQueryBindings[1])
	}
}

func TestConvertEndpointPathParamsTakePrecedenceOverQuery(t *testing.T) {
	ep := apigen.EndpointInfo{
		Name:   "tenantConnections",
		Path:   "/api/tenants/{tenantId}/connections",
		Method: "GET",
		PathParams: []apigen.Param{
			{Name: "tenantId", Type: "Identity.UUID", Required: true},
		},
	}

	converted := convertEndpoint(ep, false, "")

	if len(converted.PathArgs) != 1 || converted.PathArgs[0] != "tenant_id" {
		t.Fatalf("unexpected path args: %v", converted.PathArgs)
	}
	if len(converted.PathQueryBindings) != 0 {
		t.Fatalf("expected no query bindings, got %v", converted.PathQueryBindings)
	}
}

func float64Ptr(value float64) *float64 {
	return &value
}

func paginatedListEndpoint() apigen.EndpointInfo {
	return apigen.EndpointInfo{
		Name:          "tenantConnectorTaps",
		Namespace:     "tenant-connector-taps",
		Path:          "/api/tenant-connector-taps/tenant-connector-taps",
		Method:        "GET",
		OutputType:    "TenantConnectorTapInfo",
		OutputIsArray: true,
		QueryParams: []apigen.Param{
			{Name: "tenantConnectorId", Type: "Identity.UUID", Required: false},
			{Name: "limit", Type: "number", Required: false, ValidateMax: float64Ptr(250)},
			{Name: "offset", Type: "number", Required: false},
		},
	}
}

func TestConvertEndpointComputesListAllPagination(t *testing.T) {
	converted := convertEndpoint(paginatedListEndpoint(), false, "")

	if !converted.SupportsListAll {
		t.Fatal("expected paginated list endpoint to support a _all variant")
	}
	if converted.ListAllPageSize != 250 {
		t.Fatalf("expected page size 250 from the limit validate max, got %d", converted.ListAllPageSize)
	}
}

func TestConvertEndpointListAllRequiresDeclaredLimitMax(t *testing.T) {
	ep := paginatedListEndpoint()
	ep.QueryParams[1].ValidateMax = nil

	converted := convertEndpoint(ep, false, "")

	if converted.SupportsListAll {
		t.Fatal("a list endpoint without a schema-declared limit max must not emit a _all variant")
	}
}

func TestConvertEndpointListAllSkipsRequiredQueryParams(t *testing.T) {
	ep := paginatedListEndpoint()
	ep.QueryParams[0].Required = true

	converted := convertEndpoint(ep, false, "")

	if converted.SupportsListAll {
		t.Fatal("a list endpoint with required query params must not emit a _all variant")
	}
}

func TestConvertEndpointListAllSkipsNonArrayOutput(t *testing.T) {
	ep := paginatedListEndpoint()
	ep.OutputIsArray = false

	converted := convertEndpoint(ep, false, "")

	if converted.SupportsListAll {
		t.Fatal("a non-list endpoint must not emit a _all variant")
	}
}

func TestWriteSDKEmitsControlPlaneHelpers(t *testing.T) {
	apiOutput := &apigen.APIOutput{
		SchemaName: "helper-api",
		Endpoints:  []apigen.EndpointInfo{paginatedListEndpoint()},
	}
	clock := codegen.DefaultClock()

	sdkOutput, err := Generate(apiOutput, "", "", clock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	outDir := t.TempDir()
	if err := WriteSDKWithTools(sdkOutput, nil, outDir, t.TempDir(), clock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}

	runtimeSrc, err := os.ReadFile(filepath.Join(outDir, "src", "runtime.rs"))
	if err != nil {
		t.Fatalf("read runtime.rs: %v", err)
	}
	if !strings.Contains(string(runtimeSrc), "pub fn tenant_scoped(") {
		t.Error("runtime.rs must expose RequestOptions::tenant_scoped")
	}
	if !strings.Contains(string(runtimeSrc), "X-Tenant") {
		t.Error("tenant_scoped must set the X-Tenant header")
	}

	clientSrc, err := os.ReadFile(filepath.Join(outDir, "src", "client.rs"))
	if err != nil {
		t.Fatalf("read client.rs: %v", err)
	}
	if !strings.Contains(string(clientSrc), "pub fn with_base_url(") {
		t.Error("client.rs must expose ClientConfig::with_base_url")
	}

	errorsSrc, err := os.ReadFile(filepath.Join(outDir, "src", "errors.rs"))
	if err != nil {
		t.Fatalf("read errors.rs: %v", err)
	}
	for _, helper := range []string{
		"pub fn status_code(",
		"pub fn error_code(",
		"pub fn is_auth_denied(",
		"pub fn is_transient(",
	} {
		if !strings.Contains(string(errorsSrc), helper) {
			t.Errorf("errors.rs must expose SDKError helper %s", helper)
		}
	}
	if !strings.Contains(string(errorsSrc), "code: Option<String>") {
		t.Error("SDKError::Api must preserve the RFC 7807 code")
	}

	namespaceSrc, err := os.ReadFile(filepath.Join(outDir, "src", "namespaces", "tenant_connector_taps.rs"))
	if err != nil {
		t.Fatalf("read namespace: %v", err)
	}
	if !strings.Contains(string(namespaceSrc), "pub async fn tenant_connector_taps_all(") {
		t.Error("paginated list endpoint must emit an auto-paginating _all variant")
	}
	if !strings.Contains(string(namespaceSrc), "const PAGE_SIZE: usize = 250;") {
		t.Error("_all variant must page with the schema-declared limit max")
	}
}

func TestWriteSDKGolden(t *testing.T) {
	apiOutput := loadFixtureAPI(t)
	clock := codegen.DefaultClock()

	sdkOutput, err := Generate(apiOutput, "parable-fixture-api-sdk", naming.Default().RustTypesCrate("fixture-api"), clock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	outDir := t.TempDir()
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, t.TempDir(), clock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(outDir, "Cargo.toml"))
	if err != nil {
		t.Fatalf("read Cargo.toml: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected non-empty Cargo.toml")
	}
}
