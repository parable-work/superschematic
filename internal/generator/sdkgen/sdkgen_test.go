package sdkgen

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixturesDir = "../../loader/tsreader/testdata/services"

func loadFixtureAPI(t *testing.T) (*ir.Schema, *apigen.APIOutput, map[string]bool) {
	t.Helper()

	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}

	upstream, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}

	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     "fixture-api",
		ModulePath:     "example.com/schemas/api/fixture-api",
		TypesModule:    "example.com/schemas/types/go/fixture-api",
		IsPublic:       true,
		UpstreamSchema: "fixture-db",
		UpstreamIR:     upstream,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	if apiOutput == nil {
		t.Fatal("expected API output for fixture-api")
	}

	tsOutput, err := tsgen.Generate(schema, tsgen.Options{
		SchemaName:   "fixture-api",
		Dependencies: map[string]*ir.Schema{"fixture-db": upstream},
		DependencyPackages: map[string]string{
			"fixture-db": naming.Default().NpmTypesPackage("fixture-db"),
		},
	})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}

	return schema, apiOutput, tsgen.ParseableTypeNames(tsOutput)
}

func TestGenerateFixtureAPI(t *testing.T) {
	_, apiOutput, parseable := loadFixtureAPI(t)

	clock := codegen.DefaultClock()

	output, err := Generate(apiOutput, parseable, clock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if output == nil {
		t.Fatal("expected SDK output")
	}
	if len(output.Namespaces) == 0 {
		t.Fatal("expected at least one namespace")
	}
}

func TestToolsNamespaceUsesSDKPropertyIdentifier(t *testing.T) {
	_, apiOutput, parseable := loadFixtureAPI(t)
	clock := codegen.DefaultClock()
	sdkOutput, err := Generate(apiOutput, parseable, clock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	toolsOutput, err := GenerateTools(sdkOutput, apiOutput, clock)
	if err != nil {
		t.Fatalf("GenerateTools: %v", err)
	}
	for index := range toolsOutput.Namespaces {
		if !toolsOutput.Namespaces[index].IsScopedNS {
			toolsOutput.Namespaces[index].Name = "acme-authoring"
			break
		}
	}

	tmpl, err := template.New("tools-index.tmpl").Funcs(
		codegen.MergeTemplateFuncs(tsutil.BaseTemplateFuncs(), toolsTemplateFuncs()),
	).ParseFS(templatesFS, "templates/tools-index.tmpl")
	if err != nil {
		t.Fatalf("parse tools template: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "tools-index.tmpl", toolsOutput); err != nil {
		t.Fatalf("execute tools template: %v", err)
	}
	generated := buf.String()
	if !strings.Contains(generated, "sdk.acmeAuthoring.") {
		t.Fatalf("hyphenated namespace did not use its SDK property identifier")
	}
	if strings.Contains(generated, "sdk.acme-authoring.") {
		t.Fatalf("hyphenated namespace produced invalid TypeScript property access")
	}
}

func TestSDKConfigExposesOptionalFetch(t *testing.T) {
	_, apiOutput, parseable := loadFixtureAPI(t)
	clock := codegen.DefaultClock()
	sdkOutput, err := Generate(apiOutput, parseable, clock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	outDir := t.TempDir()
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, clock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	typesBytes, err := os.ReadFile(filepath.Join(outDir, "types.ts"))
	if err != nil {
		t.Fatalf("read types.ts: %v", err)
	}
	types := string(typesBytes)
	for _, want := range []string{
		"export type FetchLike",
		"fetch?: FetchLike",
		"export interface HttpRequestOptions",
	} {
		if !strings.Contains(types, want) {
			t.Fatalf("types.ts missing %q", want)
		}
	}
}

func TestGeneratedPackageJSONHasNoAxios(t *testing.T) {
	_, apiOutput, parseable := loadFixtureAPI(t)
	clock := codegen.DefaultClock()
	sdkOutput, err := Generate(apiOutput, parseable, clock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	outDir := t.TempDir()
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, clock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	pkg, err := os.ReadFile(filepath.Join(outDir, "package.json"))
	if err != nil {
		t.Fatalf("read package.json: %v", err)
	}
	if strings.Contains(string(pkg), "axios") {
		t.Fatalf("generated package.json still depends on axios:\n%s", pkg)
	}
}

func TestWriteSDKGolden(t *testing.T) {
	_, apiOutput, parseable := loadFixtureAPI(t)
	clock := codegen.DefaultClock()

	sdkOutput, err := Generate(apiOutput, parseable, clock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	outDir := t.TempDir()
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, clock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}

	goldenFiles := []string{
		"package.json",
		"tsconfig.json",
		"types.ts",
		"client.ts",
		"index.ts",
		"README.md",
		"namespaces/session.ts",
		"namespaces/tenant.ts",
		"tools/index.ts",
		"tools/schema.json",
		"tools/openai.json",
		"tools/anthropic.json",
	}

	for _, name := range goldenFiles {
		got, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}

		goldenPath := filepath.Join("testdata", "golden", "fixture-api", name)
		if *update {
			if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
				t.Fatalf("mkdir golden: %v", err)
			}
			if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
				t.Fatalf("write golden %s: %v", name, err)
			}
			continue
		}

		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v (run with -update)", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept)", name)
		}
	}
}

func TestNamespaceScalarGetArgsStayPositional(t *testing.T) {
	tmpl, err := template.New("namespace.tmpl").Funcs(
		codegen.MergeTemplateFuncs(tsutil.BaseTemplateFuncs(), customTemplateFuncs()),
	).ParseFS(templatesFS, "templates/namespace.tmpl")
	if err != nil {
		t.Fatalf("parse namespace template: %v", err)
	}

	data := struct {
		SDK       *SDKOutput
		Namespace NamespaceInfo
	}{
		SDK: &SDKOutput{TypesPackage: "@schemas/web-api-types"},
		Namespace: NamespaceInfo{
			Name:      "vendors",
			ClassName: "VendorsNamespace",
			Imports: []string{
				"AcmeSlug",
				"newValidationErrors",
				"setFieldErrors",
				"validateAcmeSlugRequired",
			},
			Endpoints: []EndpointInfo{{
				Name:       "acmeVendorGrouping",
				Path:       "/api/vendors/acme-vendor-grouping",
				TSPath:     "/api/vendors/acme-vendor-grouping",
				Method:     "GET",
				OutputType: "AcmeVendorGrouping",
				ScalarArgs: []apigen.ScalarArg{
					{Name: "vendorSlug", Type: "Acme.Slug", Required: true},
					{Name: "groupingPath", Type: "string", Required: true},
				},
			}},
		},
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "namespace.tmpl", data); err != nil {
		t.Fatalf("execute namespace template: %v", err)
	}
	got := buf.String()

	requiredSnippets := []string{
		"public async acmeVendorGrouping(\n    vendorSlug: any,\n    groupingPath: any,\n    signal?: any,",
		"let scalarInput = {\n      vendorSlug,\n      groupingPath,\n    };",
		"if (typeof vendorSlug === 'object' && vendorSlug !== null && !Array.isArray(vendorSlug))",
		"requestSignal = groupingPath;",
		"validateAcmeSlugRequired(scalarInput.vendorSlug)",
		"if (scalarInput.groupingPath === undefined || scalarInput.groupingPath === null)",
		"const scalarParams = { ...scalarInput };",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(got, snippet) {
			t.Fatalf("expected namespace output to contain snippet:\n%s", snippet)
		}
	}
	if strings.Contains(got, "input: any") {
		t.Fatal("scalar GET endpoint rendered object-shaped input")
	}
}

func TestNamespaceBodyInputUsesGeneratedType(t *testing.T) {
	tmpl, err := template.New("namespace.tmpl").Funcs(
		codegen.MergeTemplateFuncs(tsutil.BaseTemplateFuncs(), customTemplateFuncs()),
	).ParseFS(templatesFS, "templates/namespace.tmpl")
	if err != nil {
		t.Fatalf("parse namespace template: %v", err)
	}

	data := struct {
		SDK       *SDKOutput
		Namespace NamespaceInfo
	}{
		SDK: &SDKOutput{TypesPackage: "@schemas/web-api-types"},
		Namespace: NamespaceInfo{
			Name:      "acme-authoring",
			ClassName: "AcmeAuthoringNamespace",
			Imports: []string{
				"PrepareAcmeProposalInput",
				"PrepareAcmeProposalResult",
				"validatePrepareAcmeProposalInput",
			},
			Endpoints: []EndpointInfo{{
				Name:          "prepareAdminAcmeProposal",
				Path:          "/api/acme/proposals/{proposalId}/actions/prepare",
				TSPath:        "/api/acme/proposals/${proposalId}/actions/prepare",
				Method:        "POST",
				InputType:     "PrepareAcmeProposalInput",
				OutputType:    "PrepareAcmeProposalResult",
				HasInput:      true,
				InputRequired: true,
				PathParams: []PathParam{{
					Name:   "proposalId",
					TSName: "proposalId",
					TSType: "string",
				}},
			}},
		},
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "namespace.tmpl", data); err != nil {
		t.Fatalf("execute namespace template: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "input: PrepareAcmeProposalInput,") {
		t.Fatalf("generated SDK mutation input is not typed:\n%s", got)
	}
	if strings.Contains(got, "input: any,") {
		t.Fatal("generated SDK mutation input fell back to any")
	}
}

func TestConvertEndpointUsesPathAndMethod(t *testing.T) {
	got := convertEndpoint(apigen.EndpointInfo{
		Name:   "getTenant",
		Path:   "/api/tenant/{tenantId}",
		Method: "GET",
		PathParams: []apigen.Param{
			{Name: "tenantId", Type: "Identity.UUID"},
		},
	}, nil)

	if got.Path != "/api/tenant/{tenantId}" {
		t.Errorf("Path = %q", got.Path)
	}
	if got.Method != "GET" {
		t.Errorf("Method = %q", got.Method)
	}
	if got.TSPath != "/api/tenant/${tenantId}" {
		t.Errorf("TSPath = %q", got.TSPath)
	}
}
