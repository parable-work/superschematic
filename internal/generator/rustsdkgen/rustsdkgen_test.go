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

func TestConvertEndpointPreservesArrayQueryType(t *testing.T) {
	ep := apigen.EndpointInfo{
		Name: "listItems", Path: "/api/items", Method: "GET",
		QueryParams: []apigen.Param{{Name: "stage", Type: "string", IsArray: true}},
	}
	converted := convertEndpoint(ep, false, "")
	if len(converted.QueryParams) != 1 || converted.QueryParams[0].RustType != "Vec<String>" ||
		!converted.QueryParams[0].IsArray {
		t.Fatalf("array query type = %#v", converted.QueryParams)
	}
}

func TestRuntimeListMinimumDropsZero(t *testing.T) {
	zero, one := 0, 1
	if runtimeListMinimum(&zero) != nil {
		t.Fatal("a zero list minimum must not generate an unsigned comparison")
	}
	if got := runtimeListMinimum(&one); got == nil || *got != 1 {
		t.Fatal("a positive list minimum was lost")
	}
	if runtimeListMinimum(nil) != nil {
		t.Fatal("an absent list minimum must stay absent")
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
	if !strings.Contains(string(runtimeSrc), "pub fn with_header(") {
		t.Error("runtime.rs must expose RequestOptions::with_header")
	}
	if !strings.Contains(string(runtimeSrc), "request.header(header_name, header_value.clone())") {
		t.Error("with_header must set the named header")
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

func intPtr(value int) *int {
	return &value
}

// TestArrayQueryParamsValidateEachItem: the server splits an array query
// parameter into items and checks each one (routes.tmpl), so the SDK checks
// each typed item and counts the typed list. It must not check the joined
// "a,b" text: the comma fails a pattern and a length limit, a list of
// numbers does not parse as one number, and an item with a comma in it
// counts twice.
func TestArrayQueryParamsValidateEachItem(t *testing.T) {
	endpoint := apigen.EndpointInfo{
		Name:          "listItems",
		Namespace:     "items",
		Path:          "/api/items",
		Method:        "GET",
		OutputType:    "string",
		OutputIsArray: true,
		QueryParams: []apigen.Param{
			{Name: "tags", Type: "string", IsArray: true, Required: true, ValidatePattern: "^[a-z]+$", ValidateMinLength: intPtr(2), ValidateMaxLength: intPtr(3), ValidateListMin: intPtr(1), ValidateListMax: intPtr(2)},
			{Name: "scores", Type: "number", IsArray: true, ValidateMin: float64Ptr(1), ValidateMax: float64Ptr(5)},
		},
	}
	upload := endpoint
	upload.Name, upload.Method, upload.HasFileUpload = "uploadItems", "POST", true
	upload.FileUploadFields = []apigen.FileUploadField{{Name: "file", GoName: "File", Required: true}}
	apiOutput := &apigen.APIOutput{SchemaName: "items-api", Endpoints: []apigen.EndpointInfo{endpoint, upload}}
	clock := codegen.DefaultClock()
	sdkOutput, err := Generate(apiOutput, "", "", clock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	outDir := t.TempDir()
	if err := WriteSDKWithTools(sdkOutput, nil, outDir, t.TempDir(), clock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	source, err := os.ReadFile(filepath.Join(outDir, "src", "namespaces", "items.rs"))
	if err != nil {
		t.Fatalf("read namespace: %v", err)
	}
	generated := string(source)
	// Both the JSON and the multipart method validate the same way.
	for want, count := range map[string]int{
		"for query_param_item in query_param_value.iter() {":           4,
		"let query_param_item_text = query_param_item.to_string();":    4,
		"if !query_param_pattern.is_match(&query_param_item_text) {":   2,
		"if query_param_item_text.len() < 2 {":                         2,
		"if query_param_item_text.len() > 3 {":                         2,
		"if query_param_value.len() < 1 {":                             2,
		"if query_param_value.len() > 2 {":                             2,
		"let query_param_item_number = query_param_item_text":          2,
		"if query_param_item_number < 1.0 {":                           2,
		"if query_param_item_number > 5.0 {":                           2,
		`query_params.push(("tags".to_string(), query_param_text));`:   2,
		`query_params.push(("scores".to_string(), query_param_text));`: 2,
	} {
		if got := strings.Count(generated, want); got != count {
			t.Errorf("namespace has %d of %q, want %d", got, want, count)
		}
	}
	for _, unwanted := range []string{
		"query_param_pattern.is_match(&query_param_text)",
		"query_param_text.len()",
		"query_param_text.split(',')",
		"let query_param_number = query_param_text",
	} {
		if strings.Contains(generated, unwanted) {
			t.Errorf("namespace validates the joined array text: found %q", unwanted)
		}
	}
}

func TestWriteSDKGolden(t *testing.T) {
	apiOutput := loadFixtureAPI(t)
	clock := codegen.DefaultClock()

	sdkOutput, err := Generate(apiOutput, "schemas-fixture-api-sdk", naming.Default().RustTypesCrate("fixture-api"), clock)
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
