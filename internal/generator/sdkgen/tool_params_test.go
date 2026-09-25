package sdkgen

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

var toolParamsClock = codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

// loadToolParamsAPI loads fixture-tool-params-api, whose tools take enums,
// object types, scalars and primitives alone, optional, in lists, lists of
// lists and maps, and adds the union Fill (Swatch | Palette) as the input
// field fill: the TypeScript schema form declares no unions.
func loadToolParamsAPI(t *testing.T) (*ir.Schema, *apigen.APIOutput, map[string]bool) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-tool-params-api"))
	if err != nil {
		t.Fatalf("load fixture-tool-params-api: %v", err)
	}
	schema.Unions["Fill"] = &ir.UnionDef{Name: "Fill", Comment: "A swatch or a palette.", Types: []string{"Swatch", "Palette"}}
	input := schema.Types["PaintInput"]
	input.Fields = append(input.Fields, &ir.FieldDef{Name: "fill", TypeRef: ir.TypeRef{Name: "Fill"}, Required: true})

	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: "fixture-tool-params-api",
		Clock:      toolParamsClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: "fixture-tool-params-api", Clock: toolParamsClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	return schema, apiOutput, tsgen.ParseableTypeNames(tsOutput)
}

// TestWriteToolsGoldenToolParams pins tools/index.ts of
// fixture-tool-params-api: every tool parameter typed as the SDK method it
// is passed to takes it. Regenerate with:
// go test ./internal/generator/sdkgen -run TestWriteToolsGoldenToolParams -update
func TestWriteToolsGoldenToolParams(t *testing.T) {
	_, apiOutput, parseable := loadToolParamsAPI(t)
	sdkOutput, err := Generate(apiOutput, parseable, toolParamsClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	outDir := t.TempDir()
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, toolParamsClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGolden(t, outDir, filepath.Join("testdata", "golden", "fixture-tool-params-api"), "tools/index.ts")
}

// TestToolParamsSDKCompiles type-checks the TypeScript SDK of
// fixture-tool-params-api, tools/index.ts included, against its generated
// types package: invokeTool passes each tool's parameters to an SDK method
// that takes the types package's enums, object types, unions and scalars.
func TestToolParamsSDKCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}

	schema, apiOutput, parseable := loadToolParamsAPI(t)
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: "fixture-tool-params-api", Clock: toolParamsClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	typesRoot := filepath.Join(tempRoot, "types", "typescript")
	typesDir := filepath.Join(typesRoot, "fixture-tool-params-api")
	if err := tsgen.SetScalarLibSpec(tsOutput, paths, typesDir); err != nil {
		t.Fatalf("set superscalar spec: %v", err)
	}
	if err := tsgen.WriteTypes(tsOutput, typesDir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	if err := tsgen.WriteWorkspaceRoot(typesRoot, naming.Naming{}); err != nil {
		t.Fatalf("write workspace root: %v", err)
	}
	install := exec.Command(bunPath, "install")
	install.Dir = typesRoot
	if out, err := install.CombinedOutput(); err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun install failed for the types package (likely offline): %v\n%s", err, out))
	}
	build := exec.Command(bunPath, "x", "tsc")
	build.Dir = typesDir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("types package does not type-check: %v\n%s", err, out)
	}

	sdkOutput, err := Generate(apiOutput, parseable, toolParamsClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	sdkDir := filepath.Join(tempRoot, "sdk", "typescript", "fixture-tool-params-api")
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, toolParamsClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	if err := CompileSDK(sdkDir, typesDir); err != nil {
		t.Fatalf("generated SDK does not type-check: %v", err)
	}
	typecheckTools(t, bunPath, sdkDir)
}
