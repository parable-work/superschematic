package sdkgen

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
)

var mcpClock = codegen.FixedClock(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC))

// loadMCPFixture generates the API output of fixture-mcp: visible, hidden
// and unclassified operations, all three replay modes, a nested object, a
// typed map and array query parameters.
func loadMCPFixture(t *testing.T, hooks ...apigen.ToolHook) (*apigen.APIOutput, map[string]bool) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-mcp"))
	if err != nil {
		t.Fatalf("load fixture-mcp: %v", err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  "fixture-mcp",
		ModulePath:  "example.com/schemas/api/fixture-mcp",
		TypesModule: "example.com/schemas/types/go/fixture-mcp",
		ToolHooks:   hooks,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: "fixture-mcp"})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	return apiOutput, tsgen.ParseableTypeNames(tsOutput)
}

func writeMCPTools(t *testing.T, hooks ...apigen.ToolHook) string {
	t.Helper()
	apiOutput, parseable := loadMCPFixture(t, hooks...)
	sdkOutput, err := Generate(apiOutput, parseable, mcpClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	outDir := t.TempDir()
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, mcpClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	return outDir
}

// TestWriteToolsGoldenMCP pins every tool document of the TypeScript SDK
// for fixture-mcp. Regenerate with:
// go test ./internal/generator/sdkgen -run TestWriteToolsGoldenMCP -update
func TestWriteToolsGoldenMCP(t *testing.T) {
	outDir := writeMCPTools(t)
	for _, name := range []string{
		"tools/index.ts",
		"tools/schema.json",
		"tools/mcp-audit.json",
		"tools/mcp-binding.json",
		"tools/openai.json",
		"tools/anthropic.json",
	} {
		compareGolden(t, outDir, filepath.Join("testdata", "golden", "fixture-mcp"), name)
	}
}

func compareGolden(t *testing.T, outDir, goldenDir, name string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(outDir, name))
	if err != nil {
		t.Fatalf("read generated %s: %v", name, err)
	}
	goldenPath := filepath.Join(goldenDir, name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update)", name, err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs from golden (run with -update to accept)", name)
	}
}
