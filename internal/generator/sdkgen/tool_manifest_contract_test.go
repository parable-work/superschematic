package sdkgen

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// The tool documents are rendered by text/templates, so nothing in the Go
// type system ties the keys they emit to the ir wire types a consumer
// decodes. This test is that tie: every key tools/schema.json and
// tools/mcp-binding.json carry must decode into ir.ToolManifest and
// ir.ToolBindingManifest with unknown fields refused, and re-encoding
// those structs must reproduce the rendered document as parsed JSON.
func TestToolManifestsRoundTripThroughIRWireTypes(t *testing.T) {
	outDir := writeMCPTools(t)

	t.Run("tools/schema.json", func(t *testing.T) {
		rendered := readRendered(t, outDir, "tools/schema.json")
		var manifest ir.ToolManifest
		decodeDisallowingUnknownFields(t, rendered, &manifest)
		if len(manifest.Tools) != 6 {
			t.Fatalf("decoded %d tools, want 6", len(manifest.Tools))
		}
		get, found := manifestTool(manifest, "order.getOrder")
		if !found || get.MCP == nil || get.MCP.Handle != "get_order" || get.MCP.Icon == nil ||
			get.Replay == nil || get.Replay.Mode != "read_only" || len(get.Guidance.Errors) != 1 {
			t.Fatalf("order.getOrder did not decode its mcp, replay and guidance: %+v", get)
		}
		for _, tool := range manifest.Tools {
			if !strings.HasPrefix(tool.InputSchemaDigest, "sha256:") {
				t.Fatalf("%s decoded digest %q", tool.Name, tool.InputSchemaDigest)
			}
		}
		if del, _ := manifestTool(manifest, "order.deleteOrder"); del.MCP == nil || !del.MCP.Hidden || del.MCP.Icon != nil {
			t.Fatalf("order.deleteOrder decoded as %+v", del.MCP)
		}
		if export, _ := manifestTool(manifest, "order.exportOrders"); export.MCP != nil || export.Replay != nil {
			t.Fatalf("order.exportOrders decoded as %+v", export)
		}
		assertManifestRoundTrip(t, rendered, manifest, len(manifest.Tools))
	})

	t.Run("tools/mcp-binding.json", func(t *testing.T) {
		rendered := readRendered(t, outDir, "tools/mcp-binding.json")
		var manifest ir.ToolBindingManifest
		decodeDisallowingUnknownFields(t, rendered, &manifest)
		if manifest.APIID != "fixture-mcp" || len(manifest.Tools) != 6 {
			t.Fatalf("binding manifest decoded as %+v", manifest)
		}
		assertManifestRoundTrip(t, rendered, manifest, len(manifest.Tools))
	})
}

// TestInputSchemaDigestHashesTheEncodedArguments: a tool's digest is the
// sha256 of its argument schema as toolsutil encodes it, so it changes
// with a vendor key a hook adds.
func TestInputSchemaDigestHashesTheEncodedArguments(t *testing.T) {
	apiOutput, parseable := loadMCPFixture(t)
	sdkOutput, err := Generate(apiOutput, parseable, mcpClock)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := GenerateTools(sdkOutput, apiOutput, mcpClock)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		encoded, err := json.Marshal(tool.Parameters)
		if err != nil {
			t.Fatal(err)
		}
		if want := fmt.Sprintf("sha256:%x", sha256.Sum256(encoded)); tool.InputSchemaDigest != want {
			t.Fatalf("%s digest = %s, want %s", tool.Name, tool.InputSchemaDigest, want)
		}
	}
}

func readRendered(t *testing.T, outDir, name string) []byte {
	t.Helper()
	rendered, err := os.ReadFile(filepath.Join(outDir, name))
	if err != nil {
		t.Fatalf("read rendered %s: %v", name, err)
	}
	return rendered
}

func decodeDisallowingUnknownFields(t *testing.T, rendered []byte, target any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(rendered))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatalf("the rendered document has a key the ir wire type lacks: %v", err)
	}
}

func manifestTool(manifest ir.ToolManifest, name string) (ir.ToolManifestTool, bool) {
	for _, tool := range manifest.Tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return ir.ToolManifestTool{}, false
}

// assertManifestRoundTrip re-encodes the decoded wire struct and compares it
// with the rendered document as parsed JSON, tool by tool and then whole.
func assertManifestRoundTrip(t *testing.T, rendered []byte, decoded any, toolCount int) {
	t.Helper()
	reencoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("re-encode wire struct: %v", err)
	}
	want := parseJSONObject(t, rendered)
	got := parseJSONObject(t, reencoded)

	wantTools, _ := want["tools"].([]any)
	gotTools, _ := got["tools"].([]any)
	if len(wantTools) != toolCount || len(gotTools) != toolCount {
		t.Fatalf("tool counts differ: rendered %d, decoded %d, re-encoded %d", len(wantTools), toolCount, len(gotTools))
	}
	for index := range wantTools {
		if !reflect.DeepEqual(wantTools[index], gotTools[index]) {
			t.Fatalf("tool %d does not round-trip through the ir wire type\nrendered:   %s\nre-encoded: %s",
				index, indentJSON(t, wantTools[index]), indentJSON(t, gotTools[index]))
		}
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("the document header does not round-trip through the ir wire type\nrendered:   %s\nre-encoded: %s",
			indentJSON(t, want), indentJSON(t, got))
	}
}

func parseJSONObject(t *testing.T, encoded []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatalf("parse JSON object: %v", err)
	}
	return value
}

func indentJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("indent JSON: %v", err)
	}
	return string(encoded)
}

// TestToolReplayContractFailsTheBuild: a replay pointer that does not
// resolve to a required argument of the right type fails tool generation.
func TestToolReplayContractFailsTheBuild(t *testing.T) {
	for _, test := range []struct {
		pointer string
		want    string
	}{
		{"/missing", `operation OrderOpenReturnHandler replay contract: idempotency pointer "/missing": does not resolve at segment "missing"`},
		{"/pickup/postalCode", `segment "postalCode" is optional`},
		{"/reason/x", `segment "reason" is not a concrete object`},
	} {
		apiOutput, parseable := loadMCPFixture(t)
		for i := range apiOutput.Endpoints {
			if apiOutput.Endpoints[i].Name == "openReturn" {
				docs := *apiOutput.Endpoints[i].Docs
				docs.IdempotencyKeyPointers = []string{test.pointer}
				apiOutput.Endpoints[i].Docs = &docs
			}
		}
		sdkOutput, err := Generate(apiOutput, parseable, mcpClock)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := GenerateTools(sdkOutput, apiOutput, mcpClock); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("pointer %s: err = %v, want %q", test.pointer, err, test.want)
		}
	}
}
