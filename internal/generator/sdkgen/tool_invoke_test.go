package sdkgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// loadToolInvokeAPI loads fixture-tool-invoke-api, whose SDK methods take
// body arguments without an input type, an input type with snake_case
// fields, and a file upload. The core scalar package has no file-upload
// scalar, so Network.Url is marked one, as an extension's catalog would.
func loadToolInvokeAPI(t *testing.T) (*ir.Schema, *apigen.APIOutput, map[string]bool) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-tool-invoke-api"))
	if err != nil {
		t.Fatalf("load fixture-tool-invoke-api: %v", err)
	}
	url := schema.Scalars["Network.Url"]
	if url == nil {
		t.Fatal("fixture-tool-invoke-api does not use Network.Url")
	}
	url.FileUpload = &ir.FileUploadConfig{Category: "file"}

	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: "fixture-tool-invoke-api",
		Clock:      toolParamsClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: "fixture-tool-invoke-api", Clock: toolParamsClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	return schema, apiOutput, tsgen.ParseableTypeNames(tsOutput)
}

// TestWriteToolsGoldenToolInvoke pins tools/index.ts of
// fixture-tool-invoke-api: how invokeTool calls each SDK method. Regenerate
// with:
// go test ./internal/generator/sdkgen -run TestWriteToolsGoldenToolInvoke -update
func TestWriteToolsGoldenToolInvoke(t *testing.T) {
	_, apiOutput, parseable := loadToolInvokeAPI(t)
	sdkOutput, err := Generate(apiOutput, parseable, toolParamsClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	outDir := t.TempDir()
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, toolParamsClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	for _, name := range []string{"tools/index.ts", "tools/mcp-binding.json", "tsconfig.json"} {
		compareGolden(t, outDir, filepath.Join("testdata", "golden", "fixture-tool-invoke-api"), name)
	}
}

// TestToolInvokeSDKCompiles builds the TypeScript SDK of
// fixture-tool-invoke-api against its generated types package with the
// package's own build, which type-checks tools/index.ts, and then runs
// invokeTool against the SDK over a stubbed fetch
// (test_invoke_tool.js): each tool reaches its route with the body, query
// and path the SDK method sends, and a file upload is refused.
func TestToolInvokeSDKCompiles(t *testing.T) {
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

	schema, apiOutput, parseable := loadToolInvokeAPI(t)
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: "fixture-tool-invoke-api", Clock: toolParamsClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	typesRoot := filepath.Join(tempRoot, "types", "typescript")
	typesDir := filepath.Join(typesRoot, "fixture-tool-invoke-api")
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
	sdkDir := filepath.Join(tempRoot, "sdk", "typescript", "fixture-tool-invoke-api")
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, toolParamsClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	if err := CompileSDK(sdkDir, typesDir); err != nil {
		t.Fatalf("generated SDK does not type-check: %v", err)
	}
	requireToolsBuilt(t, sdkDir)

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test file path")
	}
	run := exec.Command(bunPath, "test", filepath.Join(filepath.Dir(currentFile), "test_invoke_tool.js"))
	run.Env = append(os.Environ(), "SDK_DIR="+sdkDir)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("invokeTool runtime test failed: %v\n%s", err, out)
	}
}
