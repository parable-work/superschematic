package sdkgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
)

// TestPathParamsSDKCompilesAndRuns type-checks the TypeScript SDK of
// fixture-nested-arrays-api, with grid.cell added, against its generated
// types package, then runs test_path_params.js: each path value, one with
// %, /, ?, # or non-ASCII text among them, is sent as one path segment,
// percent-encoded once as encodeURIComponent writes it, and a server that
// decodes it once receives the value passed.
func TestPathParamsSDKCompilesAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun not available: %v", err))
	}
	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}

	schema, apiOutput, parseable := loadNestedArraysAPI(t, sdktest.AddCellOperation)
	typesDir := writeTypesPackage(t, bunPath, schema, "fixture-nested-arrays-api", tempRoot)

	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	sdkDir := filepath.Join(tempRoot, "sdk", "typescript", "fixture-nested-arrays-api")
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	if err := CompileSDK(sdkDir, typesDir); err != nil {
		t.Fatalf("generated SDK does not type-check: %v", err)
	}

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test file path")
	}
	run := exec.Command(bunPath, "test", filepath.Join(filepath.Dir(currentFile), "test_path_params.js"))
	run.Env = append(os.Environ(), "SDK_DIR="+sdkDir)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("path params SDK runtime test failed: %v\n%s", err, out)
	}
}
