package toolsutil_test

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/toolsutil"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
	"github.com/parable-work/superschematic/internal/loader"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixturesDir = "../../loader/tsreader/testdata/services"

// toolSchema is one operation's tool schemas as the SDK tool documents
// carry them: the argument schema, its digest and the return schema.
type toolSchema struct {
	Name              string                     `json:"name"`
	InputSchemaDigest string                     `json:"inputSchemaDigest"`
	Parameters        toolsutil.JSONSchemaObject `json:"parameters"`
	Returns           toolsutil.JSONSchemaReturn `json:"returns"`
}

// TestToolSchemasGoldenNestedArrays pins the tool schemas of
// fixture-nested-arrays-api: input type fields, a body argument and a bare
// response that are arrays of arrays render as items of items. Each
// operation is built the way the SDK tool generators build it, with the
// argument's list shape passed through. Regenerate with:
// go test ./internal/generator/toolsutil -run TestToolSchemasGoldenNestedArrays -update
func TestToolSchemasGoldenNestedArrays(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-nested-arrays-api"))
	if err != nil {
		t.Fatalf("load fixture-nested-arrays-api: %v", err)
	}
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: "fixture-nested-arrays-api",
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	inputTypeFields := toolsutil.BuildInputTypeFieldsMap(output)
	var tools []toolSchema
	for _, endpoint := range output.Endpoints {
		var pathParams []toolsutil.ToolPathParam
		for _, param := range endpoint.PathParams {
			pathParams = append(pathParams, toolsutil.ToolPathParam{Name: param.Name, TSName: tsutil.ToCamelCase(param.Name), Type: param.Type})
		}
		var queryArgs []toolsutil.ToolQueryArg
		for _, param := range endpoint.QueryParams {
			queryArgs = append(queryArgs, toolsutil.ToolQueryArg{
				Name: param.Name, TSName: tsutil.ToCamelCase(param.Name), Type: param.Type,
				Required: param.Required, IsArray: param.IsArray, IsMap: param.IsMap,
				ValidateMin: param.ValidateMin, ValidateMax: param.ValidateMax,
				ValidateMinLength: param.ValidateMinLength, ValidateMaxLength: param.ValidateMaxLength,
				ValidateListMin: param.ValidateListMin, ValidateListMax: param.ValidateListMax,
				ValidatePattern: param.ValidatePattern,
			})
		}
		var scalarArgs []toolsutil.ToolScalarArg
		for _, arg := range endpoint.ScalarArgs {
			scalarArgs = append(scalarArgs, toolsutil.ToolScalarArg{
				Name: arg.Name, TSName: tsutil.ToCamelCase(arg.Name), Type: arg.Type, Required: arg.Required,
				IsArray: arg.IsArray, IsArrayOfArrays: arg.IsArrayOfArrays,
			})
		}
		parameters := toolsutil.BuildParametersSchema(
			pathParams, queryArgs, endpoint.HasInput, endpoint.InputType, inputTypeFields, output.TypeUnions, output.TypeEnums,
			scalarArgs, endpoint.Encrypted, output.Scalars,
			func(name, tsName string) string {
				if tsName != "" {
					return tsName
				}
				return tsutil.ToCamelCase(name)
			},
			output.ToolKeys,
		)
		encoded, err := json.Marshal(parameters)
		if err != nil {
			t.Fatalf("%s: encode parameters: %v", endpoint.Name, err)
		}
		tools = append(tools, toolSchema{
			Name:              endpoint.Namespace + "." + endpoint.Name,
			InputSchemaDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(encoded)),
			Parameters:        parameters,
			Returns:           toolsutil.BuildReturnSchemaAtDepth(endpoint.OutputType, endpoint.OutputArrayDepth(), output.Scalars),
		})
	}

	got, err := json.MarshalIndent(tools, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	goldenPath := filepath.Join("testdata", "golden", "fixture-nested-arrays-api", "tools.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("tools.json differs from golden (run with -update to accept)\ngot:\n%s", got)
	}
}
