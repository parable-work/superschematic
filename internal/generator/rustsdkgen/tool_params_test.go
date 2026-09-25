package rustsdkgen

import (
	"os"
	"path/filepath"
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

// TestToolParamsSchema: the Rust tool documents build their argument
// schemas as the TypeScript ones do, so an enum lists its values at every
// depth and a map body argument keeps its map shape.
func TestToolParamsSchema(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-tool-params-api"))
	if err != nil {
		t.Fatalf("load fixture-tool-params-api: %v", err)
	}
	schema.Unions["Fill"] = &ir.UnionDef{Name: "Fill", Comment: "A swatch or a palette.", Types: []string{"Swatch", "Palette"}}
	input := schema.Types["PaintInput"]
	input.Fields = append(input.Fields, &ir.FieldDef{Name: "fill", TypeRef: ir.TypeRef{Name: "Fill"}, Required: true})

	clock := codegen.FixedClock(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC))
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: "fixture-tool-params-api",
		Clock:      clock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	sdkOutput, err := Generate(apiOutput, "schemas-fixture-tool-params-api-sdk", naming.Default().RustTypesCrate("fixture-tool-params-api"), clock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	outDir := t.TempDir()
	if err := WriteSDKWithTools(sdkOutput, apiOutput, outDir, t.TempDir(), clock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	document, err := os.ReadFile(filepath.Join(outDir, "tools", "schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	toolstest.CheckToolParamsSchema(t, document)
}
