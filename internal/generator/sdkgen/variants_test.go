package sdkgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// variantAPI is the API of fixture-variant-field-db's steps, whose result
// is a variant field (D149).
const variantAPI = "fixture-variant-field-api"

// variantAPIGolden is fixture-variant-field-api's SDK, whose types package
// depends on fixture-variant-field-db's, which declares the union.
func variantAPIGolden(t *testing.T) goldenSDK {
	schema, db, apiOutput, parseable := loadBehaviorAPI(t, variantAPI, "fixture-variant-field-db")
	return goldenSDK{
		apiOutput: apiOutput,
		parseable: parseable,
		clock:     nestedArraysClock,
		types: []goldenTypes{
			{name: variantAPI, schema: schema, deps: map[string]*ir.Schema{"fixture-variant-field-db": db}},
			{name: "fixture-variant-field-db", schema: db},
		},
	}
}

// TestVariantSDKGolden pins every file of the TypeScript SDK for
// fixture-variant-field-api, its tool documents included: createStep takes
// a CreateStepInput, whose result is the database's StepResult, and parses
// its StepView answer, the result read as the member its kind names
// (parseStepResultFromJSONBy in the types package).
// TestEveryGoldenSDKTypeChecks type-checks it. Regenerate with:
// go test ./internal/generator/sdkgen -run TestVariantSDKGolden -update
func TestVariantSDKGolden(t *testing.T) {
	_, _, apiOutput, parseable := loadBehaviorAPI(t, variantAPI, "fixture-variant-field-db")
	outDir := t.TempDir()
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", variantAPI))
	source, err := os.ReadFile(filepath.Join(outDir, "namespaces", "steps.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"createStep(", "CreateStepInput", "StepView"} {
		if !strings.Contains(string(source), want) {
			t.Errorf("namespaces/steps.ts lacks %q", want)
		}
	}
}
