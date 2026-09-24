package tsrestgen

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixturesDir = "../../loader/tsreader/testdata/services"

// goldenFiles are the generated files pinned by TestWriteAPIGolden; openapi.json
// is apigen's and is pinned by apigen's own golden.
var goldenFiles = []string{"package.json", "tsconfig.json", "index.ts", "interfaces.ts", "router.ts", "README.md"}

func loadFixtureAPI(t *testing.T) (*ir.Schema, *ir.Schema) {
	t.Helper()
	apiSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}
	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	return apiSchema, dbSchema
}

var fixedClock = codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

// extractEndpoints runs the apigen extraction the api generator shares with
// the Go server and the SDKs, with the core session provider.
func extractEndpoints(t *testing.T, apiSchema, dbSchema *ir.Schema) *apigen.APIOutput {
	t.Helper()
	apiOutput, err := apigen.Generate(apiSchema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     "fixture-api",
		IsPublic:       true,
		UpstreamSchema: "fixture-db",
		UpstreamIR:     dbSchema,
		Dependencies:   map[string]*ir.Schema{"fixture-db": dbSchema},
		Clock:          fixedClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiOutput
}

func generateFixtureAPI(t *testing.T) *APIOutput {
	t.Helper()
	apiSchema, dbSchema := loadFixtureAPI(t)
	output, err := Generate(apiSchema, extractEndpoints(t, apiSchema, dbSchema), Options{
		SchemaName:   "fixture-api",
		Dependencies: map[string]*ir.Schema{"fixture-db": dbSchema},
		Clock:        fixedClock,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if output == nil {
		t.Fatal("expected TypeScript API output for fixture-api")
	}
	return output
}

func TestWriteAPIGolden(t *testing.T) {
	checkGolden(t, generateFixtureAPI(t), "fixture-api")
}

// checkGolden writes the package and compares goldenFiles with
// testdata/golden/<service> (rewriting them under -update); openapi.json
// only has to be a JSON document with paths.
func checkGolden(t *testing.T, output *APIOutput, service string) {
	t.Helper()
	outDir := t.TempDir()
	if err := WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}

	goldenDir := filepath.Join("testdata", "golden", service)
	for _, name := range goldenFiles {
		got, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}
		goldenPath := filepath.Join(goldenDir, name)
		if *update {
			if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
				t.Fatalf("create golden dir: %v", err)
			}
			if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
				t.Fatalf("write golden %s: %v", name, err)
			}
			continue
		}
		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept)", name)
		}
	}

	openapi, err := os.ReadFile(filepath.Join(outDir, "openapi.json"))
	if err != nil {
		t.Fatalf("read openapi.json: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(openapi, &doc); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	if _, ok := doc["paths"]; !ok {
		t.Error("openapi.json has no paths")
	}
}

func TestGenerateFixtureAPIShape(t *testing.T) {
	output := generateFixtureAPI(t)

	if output.PackageName != "@schemas/fixture-api-api" {
		t.Errorf("PackageName = %q", output.PackageName)
	}
	if output.TypesPackage != "@schemas/fixture-api-types" {
		t.Errorf("TypesPackage = %q", output.TypesPackage)
	}
	if len(output.Endpoints) != 6 {
		t.Fatalf("expected 6 endpoints, got %d", len(output.Endpoints))
	}
	// customHandler is @manualRouteRegistration: mounted through the hook,
	// absent from the implementation interfaces.
	if len(output.ManualEndpoints) != 1 || output.ManualEndpoints[0].Name != "customHandler" {
		t.Fatalf("expected customHandler as the only manual endpoint, got %+v", output.ManualEndpoints)
	}
	if len(output.Namespaces) != 2 || output.Namespaces[0].Name != "session" || output.Namespaces[1].Name != "tenant" {
		t.Fatalf("expected [session tenant] namespaces, got %+v", output.Namespaces)
	}
	for _, ns := range output.Namespaces {
		for _, ep := range ns.Endpoints {
			if ep.ManualRouteRegistration {
				t.Errorf("manual endpoint %s leaked into namespace %s", ep.Name, ns.Name)
			}
		}
	}

	byName := map[string]EndpointInfo{}
	for _, ep := range output.Endpoints {
		byName[ep.Name] = ep
	}

	get := byName["getTenant"]
	if get.Method != "GET" || get.Path != "/api/tenants/{id}" {
		t.Errorf("getTenant route = %s %s", get.Method, get.Path)
	}
	if len(get.PathParams) != 1 || get.PathParams[0].Kind != "uuid" || get.PathParams[0].TSType != "IdentityUUID" {
		t.Errorf("getTenant path params = %+v", get.PathParams)
	}
	if len(get.QueryParams) != 1 || get.QueryParams[0].Kind != "boolean" || !get.QueryParams[0].Required {
		t.Errorf("getTenant query params = %+v", get.QueryParams)
	}
	if get.PermsLiteral != "['tenants.read']" || !get.RequiresAuth || get.PublicRoute {
		t.Errorf("getTenant auth = perms %s auth %t public %t", get.PermsLiteral, get.RequiresAuth, get.PublicRoute)
	}
	if get.BodyLimitBytes == nil || *get.BodyLimitBytes != 1024*1024 {
		t.Errorf("getTenant body limit = %v, want 1 MiB from @bodyLimit", get.BodyLimitBytes)
	}
	// @rateLimit is set-level on TenantQueries, @timeout operation-level on
	// getTenant: the same resolution apigen gives the Go router.
	if get.RateLimitPerMinute == nil || *get.RateLimitPerMinute != 60 {
		t.Errorf("getTenant rate limit = %v, want 60 from the set's @rateLimit", get.RateLimitPerMinute)
	}
	if get.TimeoutSeconds == nil || *get.TimeoutSeconds != 5 {
		t.Errorf("getTenant timeout = %v, want 5 from @timeout", get.TimeoutSeconds)
	}
	if get.OutputType != "TenantView" {
		t.Errorf("getTenant output = %q", get.OutputType)
	}

	list := byName["listTenants"]
	if len(list.QueryParams) != 2 {
		t.Fatalf("listTenants query params = %+v", list.QueryParams)
	}
	if list.RateLimitPerMinute == nil || *list.RateLimitPerMinute != 60 || list.TimeoutSeconds != nil {
		t.Errorf("listTenants directives = rate %v timeout %v, want the set's 60/min and no timeout", list.RateLimitPerMinute, list.TimeoutSeconds)
	}
	if ids := list.QueryParams[0]; ids.Kind != "uuid" || !ids.IsArray || ids.TSType != "IdentityUUID[]" || !strings.Contains(ids.SpecLiteral, "listMin: 1") {
		t.Errorf("listTenants ids = %+v", ids)
	}
	if statuses := list.QueryParams[1]; statuses.Kind != "enum" || statuses.TSType != "TenantListStatus[]" || !strings.Contains(statuses.SpecLiteral, "enumValues: ['active', 'suspended']") {
		t.Errorf("listTenants statuses = %+v", statuses)
	}
	if list.OutputType != "TenantView[]" {
		t.Errorf("listTenants output = %q", list.OutputType)
	}

	create := byName["createTenant"]
	if !create.HasInput || create.InputType != "CreateTenantInput" || create.InputParser != "parseCreateTenantInputFromJSON" || create.InputValidator != "parseCreateTenantInputJson" {
		t.Errorf("createTenant input = %+v", create)
	}
	if create.RateLimitPerMinute != nil || create.TimeoutSeconds != nil {
		t.Errorf("createTenant (TenantMutations) must carry no directives, got rate %v timeout %v", create.RateLimitPerMinute, create.TimeoutSeconds)
	}

	me := byName["currentTenant"]
	if !me.RequiresAuth || len(me.RequiredPerms) != 0 || me.HasArgs() {
		t.Errorf("currentTenant = %+v", me)
	}

	if len(output.ValidatorImports) != 1 || output.ValidatorImports[0].Package != "@schemas/fixture-api-types" {
		t.Errorf("validator imports = %+v", output.ValidatorImports)
	}
	if strings.Join(output.ScalarImports, ",") != "IdentityUUID" {
		t.Errorf("scalar imports = %v", output.ScalarImports)
	}
}

// A file-upload operation has no multipart adapter in the TypeScript router;
// the generator refuses it unless the service registers the route by hand.
func TestGenerateRefusesFileUploadWithoutManualRegistration(t *testing.T) {
	apiSchema, dbSchema := loadFixtureAPI(t)
	fileScalar := "Upload.File"
	apiSchema.Scalars[fileScalar] = &ir.ScalarDef{Name: fileScalar, LanguagePrimitive: ir.LanguageString, FileUpload: &ir.FileUploadConfig{}}
	apiSchema.Types["UploadInput"] = &ir.TypeDef{
		Name: "UploadInput",
		Role: ir.RoleAPIInput,
		Fields: []*ir.FieldDef{{
			Name:    "file",
			TypeRef: ir.TypeRef{Name: fileScalar},
		}},
	}
	for _, set := range apiSchema.OperationSets {
		if set.Name != "TenantMutations" {
			continue
		}
		set.Operations = append(set.Operations, &ir.FieldDef{
			Name:       "uploadLogo",
			HTTPMethod: "POST",
			RestPath:   "tenants/logo",
			TypeRef:    ir.TypeRef{Name: "TenantView"},
			Arguments:  []*ir.ArgumentDef{{Name: "input", TypeRef: ir.TypeRef{Name: "UploadInput"}, Required: true}},
		})
	}
	_, err := Generate(apiSchema, extractEndpoints(t, apiSchema, dbSchema), Options{
		SchemaName:   "fixture-api",
		Dependencies: map[string]*ir.Schema{"fixture-db": dbSchema},
	})
	if err == nil || !strings.Contains(err.Error(), "@manualRouteRegistration") {
		t.Fatalf("expected a manual-registration refusal, got %v", err)
	}
}
