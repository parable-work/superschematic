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

// bodyArgsAPI is apigen's body-args-api: nameShades and placePoints, PUTs
// whose body arguments are maps of an enum, of lists of a string scalar and
// of an object type, and removeTags, a DELETE whose arguments, an optional
// map of the enum among them, the route reads from the JSON body.
const bodyArgsAPI = "body-args-api"

// TestMapArgumentsAreSentAsJSONObjects type-checks the TypeScript SDK of
// body-args-api against its generated types package, then runs
// test_map_args.js: each map argument is sent as a JSON object in the body,
// a DELETE's arguments included, and a map the route would refuse is
// refused before the request, each failure at the path the route reports
// it: the argument, name[key], name[key][i] or name[key].field.
func TestMapArgumentsAreSentAsJSONObjects(t *testing.T) {
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
	schema, err := loader.LoadService(filepath.Join("..", "apigen", "testdata", "services", bodyArgsAPI))
	if err != nil {
		t.Fatalf("load %s: %v", bodyArgsAPI, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: bodyArgsAPI,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: bodyArgsAPI, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	typesDir := writeTypesPackage(t, bunPath, schema, bodyArgsAPI, tempRoot)
	sdkOutput, err := Generate(apiOutput, tsgen.ParseableTypeNames(tsOutput), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	sdkDir := filepath.Join(tempRoot, "sdk", "typescript", bodyArgsAPI)
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
	run := exec.Command(bunPath, "test", filepath.Join(filepath.Dir(currentFile), "test_map_args.js"))
	run.Env = append(os.Environ(), "SDK_DIR="+sdkDir)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("map argument SDK runtime test failed: %v\n%s", err, out)
	}
}
