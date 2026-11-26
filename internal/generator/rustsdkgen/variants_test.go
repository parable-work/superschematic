package rustsdkgen

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// variantService is the API of fixture-variant-field-db's steps, whose
// result is a variant field (D149).
const variantService = "fixture-variant-field-api"

// TestVariantSDKGolden pins every file of the Rust SDK for
// fixture-variant-field-api, its tool documents included: create_step
// sends a CreateStepInput and reads a StepView, which the API's types crate
// decodes through its wire struct, the result as the member its kind names
// (StepResult::from_variant in the database's crate). Regenerate with:
// go test ./internal/generator/rustsdkgen -run TestVariantSDKGolden -update
func TestVariantSDKGolden(t *testing.T) {
	load := func(name string) *ir.Schema {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return schema
	}
	schema, db := load(variantService), load("fixture-variant-field-db")
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   variantService,
		ModulePath:   "example.com/schemas/api/" + variantService,
		TypesModule:  "example.com/schemas/types/go/" + variantService,
		Dependencies: map[string]*ir.Schema{"fixture-variant-field-db": db},
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "rust", variantService)
	sdkOutput, err := Generate(apiOutput, naming.Default().RustSDKCrate(variantService), naming.Default().RustTypesCrate(variantService), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, filepath.Join(root, "types", "rust", variantService), nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", variantService))
}
