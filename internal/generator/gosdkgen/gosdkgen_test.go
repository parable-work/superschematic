package gosdkgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
)

const fixturesDir = "../../loader/tsreader/testdata/services"

func loadFixtureAPI(t *testing.T) *apigen.APIOutput {
	t.Helper()

	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}
	upstream, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}

	output, err := apigen.Generate(schema, apigen.Options{
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
	return output
}

func TestWriteSDKGolden(t *testing.T) {
	apiOutput := loadFixtureAPI(t)
	clock := codegen.DefaultClock()

	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/fixture-api", "sdk", clock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	outDir := t.TempDir()
	typesDir := t.TempDir()
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, typesDir, clock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(outDir, "sdk.go"))
	if err != nil {
		t.Fatalf("read sdk.go: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected non-empty sdk.go")
	}
}

// TestEndpointParametersPreserveScalarWireTypes pins the Go SDK types of
// path and query parameters: an integer, float or boolean scalar keeps its
// JSON shape, so a tool call's numeric or boolean argument decodes into
// the query struct, and every other scalar travels as a string.
func TestEndpointParametersPreserveScalarWireTypes(t *testing.T) {
	for _, test := range []struct {
		param apigen.Param
		want  string
	}{
		{apigen.Param{Type: "Generic.Int64", IsInt: true}, "int64"},
		{apigen.Param{Type: "Generic.Probability", IsFloat: true}, "float64"},
		{apigen.Param{Type: "Acme.Flag", IsBool: true}, "bool"},
		{apigen.Param{Type: "Identity.UUID", IsUUID: true}, "string"},
		{apigen.Param{Type: "Temporal.DateTime", IsDateTime: true}, "string"},
		{apigen.Param{Type: "number"}, "float64"},
		{apigen.Param{Type: "boolean"}, "bool"},
		{apigen.Param{Type: "string"}, "string"},
	} {
		t.Run(test.param.Type, func(t *testing.T) {
			test.param.Name = "value"
			endpoint := convertEndpoint(apigen.EndpointInfo{
				Path: "/items/{value}", Method: "GET",
				PathParams: []apigen.Param{test.param}, QueryParams: []apigen.Param{test.param},
			}, false, "", "Items")
			if got := endpoint.PathParams[0].GoType; got != test.want {
				t.Errorf("path parameter type = %s, want %s", got, test.want)
			}
			if got := endpoint.QueryParams[0]; got.GoType != test.want || !got.Pointer {
				t.Errorf("optional query parameter = %+v, want *%s", got, test.want)
			}
		})
	}
}
