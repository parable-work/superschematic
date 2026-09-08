package gosdkgen

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
	upstream, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}

	output, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     "fixture-api",
		ModulePath:     "github.com/parable-platform/platform-schemas/api/fixture-api",
		TypesModule:    "github.com/parable-platform/platform-schemas/types/go/fixture-api",
		IsPublic:       true,
		UpstreamSchema: "fixture-db",
		UpstreamIR:     upstream,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return output
}

func TestWriteSDKGolden(t *testing.T) {
	apiOutput := loadFixtureAPI(t)
	clock := codegen.DefaultClock()

	sdkOutput, err := Generate(apiOutput, "github.com/parable-platform/platform-schemas/sdk/go/fixture-api", "sdk", clock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	outDir := t.TempDir()
	typesDir := t.TempDir()
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, typesDir, clock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(outDir, "sdk.go"))
	if err != nil {
		t.Fatalf("read sdk.go: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected non-empty sdk.go")
	}
}
