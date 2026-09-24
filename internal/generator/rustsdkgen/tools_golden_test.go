package rustsdkgen

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/toolsutil/toolstest"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

var update = flag.Bool("update", false, "rewrite golden files")

// writeMCPTools writes the Rust SDK of fixture-mcp, whose operations cover
// visible, hidden and unclassified tools and all three replay modes.
func writeMCPTools(t *testing.T, hooks ...apigen.ToolHook) string {
	t.Helper()
	return writeMCPToolsWith(t, apigen.ToolInvocationPolicy{}, nil, hooks...)
}

// writeMCPToolsWith is writeMCPTools under an invocation policy, with the
// loaded schema's records rewritten first when rewrite is set.
func writeMCPToolsWith(t *testing.T, invocation apigen.ToolInvocationPolicy, rewrite func(*testing.T, *ir.Schema), hooks ...apigen.ToolHook) string {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-mcp"))
	if err != nil {
		t.Fatalf("load fixture-mcp: %v", err)
	}
	if rewrite != nil {
		rewrite(t, schema)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     "fixture-mcp",
		ModulePath:     "example.com/schemas/api/fixture-mcp",
		TypesModule:    "example.com/schemas/types/go/fixture-mcp",
		ToolHooks:      hooks,
		ToolInvocation: invocation,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	clock := codegen.FixedClock(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC))
	sdkOutput, err := Generate(apiOutput, "schemas-fixture-mcp-sdk", naming.Default().RustTypesCrate("fixture-mcp"), clock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	outDir := t.TempDir()
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, t.TempDir(), clock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	return outDir
}

// TestWriteToolsGoldenMCP pins the tool documents of the Rust SDK for
// fixture-mcp. Regenerate with:
// go test ./internal/generator/rustsdkgen -run TestWriteToolsGoldenMCP -update
func TestWriteToolsGoldenMCP(t *testing.T) {
	outDir := writeMCPTools(t)
	for _, name := range []string{
		"tools/schema.json",
		"tools/mcp-audit.json",
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

// TestToolHookReachesTheRustDocuments: a hook's keys and icon edits are
// what every Rust tool document carries.
func TestToolHookReachesTheRustDocuments(t *testing.T) {
	outDir := writeMCPTools(t, apigen.ToolHook{
		Name: "vendor",
		Edit: func(_ *ir.Schema, tools *apigen.ToolSet) error {
			tools.Keys.Scalar = "x-vendor-scalar"
			tools.Keys.Parameters = []apigen.ToolKeyValue{{Key: "x-vendor-version", Value: 1}}
			for _, tool := range tools.Tools {
				if tool.MCP != nil && tool.MCP.Icon != nil {
					tool.MCP.Icon.Family = "line"
				}
			}
			return nil
		},
	})
	for _, name := range []string{"tools/schema.json", "tools/mcp-audit.json", "tools/openai.json", "tools/anthropic.json"} {
		rendered, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(rendered), apigen.DefaultToolScalarKey) {
			t.Errorf("%s still carries the core scalar key", name)
		}
		if name != "tools/mcp-audit.json" && !strings.Contains(string(rendered), `"x-vendor-version": 1,`) {
			t.Errorf("%s lacks the vendor parameter key", name)
		}
		if name != "tools/openai.json" && name != "tools/anthropic.json" && !strings.Contains(string(rendered), `"family": "line"`) {
			t.Errorf("%s lacks the icon family", name)
		}
	}
}

// TestWriteToolsUnderAnExtensionsInvocationPolicy: under an extension's
// invocation policy the Rust tool documents write its key and values where
// they write the core's, and nothing else changes.
func TestWriteToolsUnderAnExtensionsInvocationPolicy(t *testing.T) {
	core := writeMCPTools(t)
	review := writeMCPToolsWith(t, toolstest.ReviewPolicy, toolstest.UseReviewPolicy)
	read := func(dir, name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	for _, name := range []string{"tools/schema.json", "tools/mcp-audit.json"} {
		toolstest.CompareUnderReviewPolicy(t, name, read(core, name), read(review, name))
	}
	for _, name := range []string{"tools/openai.json", "tools/anthropic.json"} {
		if read(core, name) != read(review, name) {
			t.Errorf("%s changed under the review policy", name)
		}
	}
}
