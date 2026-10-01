package gosdkgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestOptionalGenericJSONNullReachesTheGoServer generates the Go types
// module, the Go API module and the Go SDK of apigen's body-args-api into
// one temp tree. It copies apigen's route test into the API module for its
// implementation and server, and runs optionalJSONServerTest beside it: the
// SDK sends an optional Generic.JSON body argument and input type field as
// null when the caller sets them to the JSON null token and leaves them out
// when unset, and the implementation receives the two apart.
func TestOptionalGenericJSONNullReachesTheGoServer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	const service = "body-args-api"
	schema, err := loader.LoadService(filepath.Join("..", "apigen", "testdata", "services", service))
	if err != nil {
		t.Fatalf("load %s: %v", service, err)
	}
	paths := testpaths.Local(t)
	typesModule := "example.com/schemas/types/go/" + service
	sdkModule := "example.com/schemas/sdk/go/" + service

	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "go", service)
	apiDir := filepath.Join(root, "api", service)
	sdkDir := filepath.Join(root, "sdk", "go", service)

	typesOutput, err := typegen.Generate(schema, typegen.Options{SchemaName: service, ModulePath: typesModule, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("typegen.Generate: %v", err)
	}
	if err := typegen.SetReplacePaths(typesOutput, paths, typesDir); err != nil {
		t.Fatalf("typegen.SetReplacePaths: %v", err)
	}
	if err := typegen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("typegen.WriteTypes: %v", err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  service,
		ModulePath:  "example.com/schemas/api/" + service,
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
	sdkOutput, err := Generate(apiOutput, sdkModule, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}

	routesTest, err := os.ReadFile(filepath.Join("..", "apigen", "testdata", "body_args_routes_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{
		"body_args_routes_test.go": routesTest,
		"optional_json_test.go":    []byte(optionalJSONServerTest),
	} {
		if err := os.WriteFile(filepath.Join(apiDir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"mod", "edit", "-replace", sdkModule + "=../../sdk/go/" + service},
		{"mod", "tidy"},
		{"vet", "./..."},
		{"test", "-count=1", "-run", "^TestTheSDK", "-v", "./..."},
	} {
		// No cmd.Env: exec then sets PWD to cmd.Dir, which keeps the
		// module's relative replace paths valid under a symlinked temp dir.
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

// optionalJSONServerTest runs in the generated API module of body-args-api
// beside apigen's route test, whose serve and tags it uses, with the Go SDK
// replaced in.
const optionalJSONServerTest = `package bodyargsapi_test

import (
	"context"
	"testing"

	sdk "example.com/schemas/sdk/go/body-args-api"
	"example.com/schemas/sdk/go/body-args-api/namespaces"
	types "example.com/schemas/types/go/body-args-api"
)

func client(t *testing.T) (*sdk.BodyArgsApiSDK, *tags) {
	t.Helper()
	server, impl := serve(t)
	client, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client, impl
}

func TestTheSDKSendsAnOptionalGenericJSONArgumentAsNullOrLeavesItOut(t *testing.T) {
	client, impl := client(t)
	ctx := context.Background()
	if _, err := client.TagNamespace.StoreDocument(ctx, namespaces.TagStoreDocumentInput{Document: types.GenericJSON("1")}); err != nil {
		t.Fatalf("StoreDocument: %v", err)
	}
	if note := impl.last["note"].(types.GenericJSON); note != nil {
		t.Errorf("an unset note reached the implementation as %q, want nil", note)
	}
	null := types.GenericJSON("null")
	if _, err := client.TagNamespace.StoreDocument(ctx, namespaces.TagStoreDocumentInput{Document: types.GenericJSON("1"), Note: &null}); err != nil {
		t.Fatalf("StoreDocument with a null note: %v", err)
	}
	if note := impl.last["note"].(types.GenericJSON); string(note) != "null" {
		t.Errorf("a null note reached the implementation as %q, want the JSON null token", note)
	}
}

func TestTheSDKSendsAnOptionalGenericJSONFieldAsNullOrLeavesItOut(t *testing.T) {
	client, impl := client(t)
	ctx := context.Background()
	if _, err := client.TagNamespace.ReviseDocument(ctx, types.DocumentRevision{Document: types.GenericJSON("1")}); err != nil {
		t.Fatalf("ReviseDocument: %v", err)
	}
	if note := impl.last["note"].(types.InputField[types.GenericJSON]); note.IsSet() {
		t.Errorf("an unset note reached the implementation set: %+v", note)
	}
	revision := types.DocumentRevision{Document: types.GenericJSON("1"), Note: types.InputField[types.GenericJSON]{Set: true, Null: true}}
	if _, err := client.TagNamespace.ReviseDocument(ctx, revision); err != nil {
		t.Fatalf("ReviseDocument with a null note: %v", err)
	}
	if note := impl.last["note"].(types.InputField[types.GenericJSON]); !note.IsNull() {
		t.Errorf("a null note reached the implementation as %+v, want null", note)
	}
}
`
