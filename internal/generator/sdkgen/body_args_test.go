package sdkgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// bodyArgsAPI is apigen's body-args-api. Its removeTags is a DELETE without
// an input type: a required list of strings, a boolean and a Generic.JSON,
// which the route reads from the JSON body, as it does a PUT's.
const bodyArgsAPI = "body-args-api"

// TestADELETEsArgumentsReachTheGoServer generates the Go types module, the
// Go API module, the TypeScript types package and the TypeScript SDK of
// body-args-api into one temp tree and type-checks the SDK. It copies
// apigen's route test into the API module, for its tags implementation and
// jsonEqual, and runs typeScriptSDKServerTest beside it: test_body_args.js
// calls removeTags against the generated route, which receives each
// argument from the JSON body, and a missing required argument is refused
// before the request.
func TestADELETEsArgumentsReachTheGoServer(t *testing.T) {
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
	paths := testpaths.Local(t)
	typesModule := "example.com/schemas/types/go/" + bodyArgsAPI
	goTypesDir := filepath.Join(tempRoot, "types", "go", bodyArgsAPI)
	apiDir := filepath.Join(tempRoot, "api", bodyArgsAPI)
	sdkDir := filepath.Join(tempRoot, "sdk", "typescript", bodyArgsAPI)

	typesOutput, err := typegen.Generate(schema, typegen.Options{SchemaName: bodyArgsAPI, ModulePath: typesModule, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("typegen.Generate: %v", err)
	}
	if err := typegen.SetReplacePaths(typesOutput, paths, goTypesDir); err != nil {
		t.Fatalf("typegen.SetReplacePaths: %v", err)
	}
	if err := typegen.WriteTypes(typesOutput, goTypesDir); err != nil {
		t.Fatalf("typegen.WriteTypes: %v", err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  bodyArgsAPI,
		ModulePath:  "example.com/schemas/api/" + bodyArgsAPI,
		TypesModule: typesModule,
		Clock:       nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	if err := apigen.SetReplacePaths(apiOutput, paths, apiDir); err != nil {
		t.Fatalf("apigen.SetReplacePaths: %v", err)
	}
	if err := apigen.WriteAPI(apiOutput, apiDir); err != nil {
		t.Fatalf("apigen.WriteAPI: %v", err)
	}

	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: bodyArgsAPI, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	tsTypesDir := writeTypesPackage(t, bunPath, schema, bodyArgsAPI, tempRoot)
	sdkOutput, err := Generate(apiOutput, tsgen.ParseableTypeNames(tsOutput), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	if err := CompileSDK(sdkDir, tsTypesDir); err != nil {
		t.Fatalf("generated SDK does not type-check: %v", err)
	}

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test file path")
	}
	script := filepath.Join(filepath.Dir(currentFile), "test_body_args.js")
	routesTest, err := os.ReadFile(filepath.Join("..", "apigen", "testdata", "body_args_routes_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"body_args_routes_test.go": string(routesTest),
		"typescript_sdk_test.go":   fmt.Sprintf(typeScriptSDKServerTest, bunPath, script, sdkDir, deleteCalls),
	} {
		if err := os.WriteFile(filepath.Join(apiDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"mod", "tidy"},
		{"vet", "./..."},
		{"test", "-count=1", "-run", "^TestTheTypeScriptSDK", "-v", "./..."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
		if args[0] == "test" {
			t.Logf("generated API tests:\n%s", out)
		}
	}
}

// deleteCalls are the arguments, as JSON, of each call of test_body_args.js
// that reaches the implementation, in order. An empty value is a
// Generic.JSON the request left out, which the implementation receives as
// nil, apart from the JSON null token.
var deleteCalls = []map[string]string{
	{"id": `"p1"`, "labels": `["c"]`, "purge": `false`, "reason": ""},
	{"id": `"p1"`, "labels": `["a", "b"]`, "purge": `true`, "reason": `{"by": ["editor"]}`},
	{"id": `"p1"`, "labels": `["c"]`, "purge": `false`, "reason": `null`},
	{"id": `"p1"`, "labels": `["d"]`, "purge": `true`, "reason": ""},
}

// typeScriptSDKServerTest runs in the generated API module of body-args-api
// beside apigen's route test, whose tags implementation and jsonEqual it
// uses. It is formatted with bun, test_body_args.js, the SDK directory and
// deleteCalls.
const typeScriptSDKServerTest = `package bodyargsapi_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	api "example.com/schemas/api/body-args-api"
	types "example.com/schemas/types/go/body-args-api"
)

func TestTheTypeScriptSDKSendsADELETEsArgumentsInTheBody(t *testing.T) {
	impl := &tags{}
	router := chi.NewRouter()
	if err := api.RegisterRoutes(router, api.Config{
		Logger:          zap.NewNop(),
		Implementations: api.Implementations{Tag: impl},
	}); err != nil {
		t.Fatal(err)
	}
	var received []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls := impl.calls
		router.ServeHTTP(w, r)
		if impl.calls > calls {
			received = append(received, impl.last)
		}
	}))
	t.Cleanup(server.Close)

	run := exec.Command(%[1]q, "test", %[2]q)
	run.Env = append(os.Environ(), "SDK_DIR="+%[3]q, "BASE_URL="+server.URL)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("TypeScript SDK round trip failed: %%v\n%%s", err, out)
	}

	want := %#[4]v
	if len(received) != len(want) {
		t.Fatalf("%%d calls reached the implementation, want %%d: a refused call must not be sent", len(received), len(want))
	}
	for i, args := range want {
		for name, value := range args {
			got := received[i][name]
			if value == "" {
				if raw, ok := got.(types.GenericJSON); !ok || raw != nil {
					t.Errorf("call %%d: %%s reached the implementation as %%#v, want it absent", i, name, got)
				}
			} else if !jsonEqual(t, got, value) {
				t.Errorf("call %%d: %%s reached the implementation as %%#v, want %%s", i, name, got, value)
			}
		}
	}
}
`
