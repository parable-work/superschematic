package gosdkgen

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
)

var update = flag.Bool("update", false, "rewrite golden files")

// writeMCPTools writes the Go SDK of fixture-mcp, whose operations cover
// visible, hidden and unclassified tools and all three replay modes.
func writeMCPTools(t *testing.T) string {
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
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	clock := codegen.FixedClock(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC))
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/fixture-mcp", "sdk", clock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	outDir := t.TempDir()
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, t.TempDir(), clock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	return outDir
}

// TestWriteToolsGoldenMCP pins the tool documents of the Go SDK for
// fixture-mcp. Regenerate with:
// go test ./internal/generator/gosdkgen -run TestWriteToolsGoldenMCP -update
func TestWriteToolsGoldenMCP(t *testing.T) {
	outDir := writeMCPTools(t)
	for _, name := range []string{
		"tools/schema.json",
		"tools/mcp-audit.json",
		"tools/mcp-binding.json",
		"tools/openai.json",
		"tools/anthropic.json",
	} {
		got, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}
		goldenPath := filepath.Join("testdata", "golden", "fixture-mcp", name)
		if *update {
			if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v (run with -update)", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept)", name)
		}
	}
}
