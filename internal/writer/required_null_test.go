package writer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestRequiredNullAndRuntimeRoundTrip: a RequiredNull field (D130) and a
// Runtime<T> field (D129) go from YAML to IR to TypeScript, JSON and YAML
// and back to equal IR. The TypeScript writer writes RequiredNull with no
// wrapper and Runtime<T> around the field's type, and imports both from the
// schema package.
func TestRequiredNullAndRuntimeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"schema.config.yaml": "name: shipments\nkind: General\noutputs: {}\n",
		"src/shipment.schema.yaml": `name: shipments
kind: General
types:
  VoidedShipment:
    name: VoidedShipment
    role: EmbeddedStruct
    strictJSON: true
    fields:
      - name: voidedAt
        typeRef: { name: "" }
        required: true
        requiredNull: true
      - name: reason
        typeRef: { name: string }
        required: true
  ShipmentsConfig:
    name: ShipmentsConfig
    role: EmbeddedStruct
    envVars: true
    fields:
      - name: LOG_LEVEL
        typeRef: { name: string }
        required: true
        runtime: true
`,
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := loader.LoadService(dir)
	if err != nil {
		t.Fatal(err)
	}
	if field := schema.Types["VoidedShipment"].Fields[0]; !field.RequiredNull || !field.Required {
		t.Fatalf("voidedAt = %+v, want a required RequiredNull field", field)
	}
	ts := writtenSource(t, schema, FormatTS)
	for _, want := range []string{"  voidedAt: RequiredNull;\n", "  LOG_LEVEL: Runtime<string>;\n", "RequiredNull, Runtime"} {
		if !strings.Contains(ts, want) {
			t.Errorf("TS output misses %q:\n%s", want, ts)
		}
	}
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		if got, want := normalizeIR(t, writeAndReload(t, schema, format)), normalizeIR(t, schema); got != want {
			t.Errorf("IR changed after a YAML -> %s round trip\nwant:\n%s\ngot:\n%s", format, want, got)
		}
	}
}
