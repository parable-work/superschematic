package gosdkgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/release"
	ir "github.com/parable-work/superschematic/ir"
)

// TestWriteSDKReleaseGolden: in a project whose naming file has no
// [paths], the SDK module a release writes for an API with a filtered
// endpoint (FilterBy, D106), whose namespace imports the HTTP runtime's
// filterparse, requires the HTTP runtime at the release's pin and replaces
// every version of it, of the schema runtime it builds on, and of the
// scalar library and the schema IR its types module reaches with its pin,
// so it resolves from the module proxy on its own (D47, amended).
// Regenerate with:
//
//	go test ./internal/generator/gosdkgen -run TestWriteSDKReleaseGolden -update
func TestWriteSDKReleaseGolden(t *testing.T) {
	apiOutput := loadFixtureAPI(t)
	// fixture-api declares no filter; giving a GET operation one has the
	// SDK import the HTTP runtime.
	marked := false
	for i := range apiOutput.Endpoints {
		if apiOutput.Endpoints[i].Method == "GET" {
			apiOutput.Endpoints[i].Filter = &apigen.FilterInfo{
				Argument: "filter", GoName: "Filter", TypeName: "Filter", Operation: apiOutput.Endpoints[i].Name,
				Fields: []apigen.FilterFieldInfo{{
					Name: "name", GoName: "Name", Kind: ir.FilterKindString,
					Operators: ir.FilterOperators(ir.FilterKindString, false),
					Value:     apigen.BodyArg{Param: apigen.Param{Name: "filter[name]", Type: "string", GoType: "string", IsString: true}, Kind: "String"},
				}},
			}
			marked = true
			break
		}
	}
	if !marked {
		t.Fatal("fixture-api declares no GET operation")
	}
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/fixture-api", "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !sdkOutput.HasFilteredEndpoints {
		t.Fatal("the SDK has no filtered endpoint")
	}
	rel := release.Release{Version: "1.2.3", ScalarGo: "v0.0.0-20260928143325-10cf493f485e"}
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "go", "fixture-api")
	typesDir := filepath.Join(root, "types", "go", "fixture-api")
	if err := os.MkdirAll(typesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SetReplacePaths(sdkOutput, naming.LocalPaths{}, sdkDir); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	SetReleasePins(sdkOutput, sdkOutput.Naming.ReleasePins(naming.LocalPaths{}, rel))
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(sdkDir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "golden", "fixture-api-release", "go.mod")
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v (run with -update)", err)
	}
	if string(got) != string(want) {
		t.Errorf("go.mod differs from %s (run with -update to accept):\n%s", golden, got)
	}
}
