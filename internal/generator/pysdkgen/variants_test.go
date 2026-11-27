package pysdkgen

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// variantService is the API of fixture-variant-field-db's steps, whose
// result is a variant field (D149).
const variantService = "fixture-variant-field-api"

// TestVariantSDKGolden pins every file of the Python SDK for
// fixture-variant-field-api: create_step sends a CreateStepInput and reads
// a StepView, whose model reads the result as the member its kind names
// (read_variant_field in the database's unions.py). Regenerate with:
// go test ./internal/generator/pysdkgen -run TestVariantSDKGolden -update
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
		Dependencies: map[string]*ir.Schema{"fixture-variant-field-db": db},
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	outDir := t.TempDir()
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDK(sdkOutput, outDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", variantService))
}
