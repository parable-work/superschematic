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
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
)

// bodyArgsService is apigen's body-args-api, whose removeTags is a DELETE
// with a required string, a list, maps and JSON-valued arguments, which
// its route reads from the JSON body.
const bodyArgsService = "body-args-api"

// TestDELETESDKSendsItsArgumentsInTheBody type-checks the TypeScript SDK of
// body-args-api against its generated types package, then runs
// test_delete_args.js: removeTags sends its arguments in the DELETE's JSON
// body, as the route reads them, and none in the query string.
func TestDELETESDKSendsItsArgumentsInTheBody(t *testing.T) {
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
	schema, err := loader.LoadService(filepath.Join("..", "apigen", "testdata", "services", bodyArgsService))
	if err != nil {
		t.Fatalf("load %s: %v", bodyArgsService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: bodyArgsService,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: bodyArgsService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	typesDir := writeTypesPackage(t, bunPath, schema, bodyArgsService, tempRoot)
	sdkOutput, err := Generate(apiOutput, tsgen.ParseableTypeNames(tsOutput), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	sdkDir := filepath.Join(tempRoot, "sdk", "typescript", bodyArgsService)
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
	run := exec.Command(bunPath, "test", filepath.Join(filepath.Dir(currentFile), "test_delete_args.js"))
	run.Env = append(os.Environ(), "SDK_DIR="+sdkDir)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("DELETE arguments SDK runtime test failed: %v\n%s", err, out)
	}
}
