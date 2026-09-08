package pysdkgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
)

const fixturesDir = "../../loader/tsreader/testdata/services"

func loadFixtureAPI(t *testing.T) *apigen.APIOutput {
	t.Helper()

	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}

	output, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  "fixture-api",
		ModulePath:  "example.com/schemas/api/fixture-api",
		TypesModule: "example.com/schemas/types/go/fixture-api",
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return output
}

func TestWriteSDKGolden(t *testing.T) {
	apiOutput := loadFixtureAPI(t)
	clock := codegen.DefaultClock()

	sdkOutput, err := Generate(apiOutput, "", "", clock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	outDir := t.TempDir()
	if err := WriteSDK(sdkOutput, outDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(outDir, sdkOutput.PackageName, "sdk.py"))
	if err != nil {
		t.Fatalf("read sdk.py: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected non-empty sdk.py")
	}
}

func TestGenerateDefaultPackageNames(t *testing.T) {
	apiOutput := loadFixtureAPI(t)
	apiOutput.SchemaName = "web-admin-api"

	sdkOutput, err := Generate(apiOutput, "", "", codegen.DefaultClock())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if sdkOutput.PackageName != "parable_web_admin_api_sdk" {
		t.Fatalf("PackageName = %q, want %q", sdkOutput.PackageName, "parable_web_admin_api_sdk")
	}
	if sdkOutput.TypesPackage != "parable_types_web_admin_api" {
		t.Fatalf("TypesPackage = %q, want %q", sdkOutput.TypesPackage, "parable_types_web_admin_api")
	}
}

func TestGenerateConfiguredPackageNames(t *testing.T) {
	apiOutput := loadFixtureAPI(t)

	sdkOutput, err := Generate(apiOutput, "custom-api-sdk", "custom-api-types", codegen.DefaultClock())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if sdkOutput.PackageName != "custom_api_sdk" {
		t.Fatalf("PackageName = %q, want %q", sdkOutput.PackageName, "custom_api_sdk")
	}
	if sdkOutput.TypesPackage != "custom_api_types" {
		t.Fatalf("TypesPackage = %q, want %q", sdkOutput.TypesPackage, "custom_api_types")
	}
}
