package gosdkgen

import (
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

const (
	scalarArgsService     = "fixture-scalar-args-api"
	scalarArgsTypesModule = "example.com/schemas/types/go/fixture-scalar-args-api"
	scalarArgsSDKModule   = "example.com/schemas/sdk/go/fixture-scalar-args-api"
)

func loadScalarArgsAPI(t *testing.T) *apigen.APIOutput {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, scalarArgsService))
	if err != nil {
		t.Fatalf("load %s: %v", scalarArgsService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  scalarArgsService,
		ModulePath:  "example.com/schemas/api/fixture-scalar-args-api",
		TypesModule: scalarArgsTypesModule,
		Clock:       nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiOutput
}

// TestScalarTypesUseTheTypesPackageNames: a scalar body argument or
// response is typed with the name the Go types package declares for the
// scalar (Identity.UserID is types.IdentityUserID), not the last segment
// of its canonical name, which the types package does not declare.
func TestScalarTypesUseTheTypesPackageNames(t *testing.T) {
	sdkOutput, err := Generate(loadScalarArgsAPI(t), scalarArgsSDKModule, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	endpoints := map[string]EndpointInfo{}
	for _, ns := range sdkOutput.Namespaces {
		for _, endpoint := range ns.Endpoints {
			endpoints[endpoint.MethodName] = endpoint
		}
	}

	lookup := endpoints["Lookup"]
	args := map[string]string{}
	for _, arg := range lookup.ScalarArgs {
		args[arg.Name] = arg.GoType
	}
	for name, want := range map[string]string{
		"owner": "types.IdentityUserID",
		"ids":   "[]types.IdentityUUID",
		"token": "types.AuthJWT",
		"note":  "string",
	} {
		if args[name] != want {
			t.Errorf("lookup argument %s is %q, want %q", name, args[name], want)
		}
	}
	if lookup.OutputGoType != "[]types.GenericJSON" {
		t.Errorf("lookup returns %q, want []types.GenericJSON", lookup.OutputGoType)
	}
	if got := endpoints["Owner"].OutputGoType; got != "types.IdentityUserID" {
		t.Errorf("owner returns %q, want types.IdentityUserID", got)
	}
	if got := qualifyType("GridView", nil); got != "types.GridView" {
		t.Errorf("an object type is %q, want types.GridView", got)
	}
}

// TestScalarArgsSDKBuilds generates the Go types module and the Go SDK of
// fixture-scalar-args-api into a temp tree laid out as a build writes it
// and builds the SDK against the types module.
func TestScalarArgsSDKBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	schema, err := loader.LoadService(filepath.Join(fixturesDir, scalarArgsService))
	if err != nil {
		t.Fatalf("load %s: %v", scalarArgsService, err)
	}
	apiOutput := loadScalarArgsAPI(t)

	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "go", scalarArgsService)
	sdkDir := filepath.Join(root, "sdk", "go", scalarArgsService)
	typesOutput, err := typegen.Generate(schema, typegen.Options{
		SchemaName: scalarArgsService,
		ModulePath: scalarArgsTypesModule,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("typegen.Generate: %v", err)
	}
	if err := typegen.SetReplacePaths(typesOutput, paths, typesDir); err != nil {
		t.Fatalf("typegen.SetReplacePaths: %v", err)
	}
	if err := typegen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("typegen.WriteTypes: %v", err)
	}
	sdkOutput, err := Generate(apiOutput, scalarArgsSDKModule, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}

	for _, args := range [][]string{{"mod", "tidy"}, {"build", "./..."}, {"vet", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = sdkDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in the generated SDK: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}
