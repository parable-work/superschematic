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
// its route reads from the JSON body, and whose nameShades and placePoints
// take maps of an enum, of lists of a string scalar and of an object type.
const bodyArgsService = "body-args-api"

// TestDELETESDKSendsItsArgumentsInTheBody runs test_delete_args.js on the
// TypeScript SDK of body-args-api (runBodyArgsSDKScript): removeTags sends
// its arguments in the DELETE's JSON body, as the route reads them, and
// none in the query string.
func TestDELETESDKSendsItsArgumentsInTheBody(t *testing.T) {
	runBodyArgsSDKScript(t, "test_delete_args.js")
}

// runBodyArgsSDKScript type-checks the TypeScript SDK of body-args-api
// against its generated types package, then runs script, a bun test file
// beside this one, with SDK_DIR set to the SDK package.
func runBodyArgsSDKScript(t *testing.T, script string) {
	t.Helper()
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
	run := exec.Command(bunPath, "test", filepath.Join(filepath.Dir(currentFile), script))
	run.Env = append(os.Environ(), "SDK_DIR="+sdkDir)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v\n%s", script, err, out)
	}
}
