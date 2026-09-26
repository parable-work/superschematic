package typegen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestStructuredJSONScalarsCompile builds a module whose type holds the
// JSON object and array scalars of the catalog (Generic.StringMap,
// Embedding.Vector) as required, optional and list fields, and runs a test
// inside it. Parse<Symbol> of a JSON array scalar decodes the core's
// canonical JSON text into the slice; before, it converted the text to the
// slice type and the module did not build. The generated type decodes the
// object or array, refuses its JSON text, and Validate reports a missing
// required one.
func TestStructuredJSONScalarsCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module build in -short mode")
	}
	const service = "structured-scalars"
	dir := filepath.Join(t.TempDir(), service)
	files := map[string]any{
		"schema.config.json": map[string]any{
			"name":    service,
			"kind":    "General",
			"outputs": map[string]any{"types": map[string]any{"go": map[string]any{"enabled": true}}},
		},
		filepath.Join("src", "structured.schema.json"): map[string]any{
			"scalars": map[string]any{
				"Generic.StringMap": map[string]any{"name": "Generic.StringMap", "languagePrimitive": "string"},
				"Embedding.Vector":  map[string]any{"name": "Embedding.Vector", "languagePrimitive": "string"},
			},
			"types": map[string]any{
				"Structured": map[string]any{
					"name": "Structured",
					"role": "EmbeddedStruct",
					"fields": []any{
						map[string]any{"name": "tags", "typeRef": map[string]any{"name": "Generic.StringMap"}, "required": true},
						map[string]any{"name": "optTags", "typeRef": map[string]any{"name": "Generic.StringMap"}},
						map[string]any{"name": "vec", "typeRef": map[string]any{"name": "Embedding.Vector"}, "required": true},
						map[string]any{"name": "vecs", "typeRef": map[string]any{"name": "Embedding.Vector", "isArray": true}},
					},
				},
			},
		},
	}
	for rel, doc := range files {
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := loader.LoadService(dir)
	if err != nil {
		t.Fatalf("load %s: %v", service, err)
	}
	output, err := Generate(schema, Options{
		SchemaName: service,
		ModulePath: "example.com/schemas/types/go/" + service,
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for _, scalar := range output.Scalars {
		if !scalar.ParseAsJSON {
			t.Errorf("%s: Parse%s must decode the core's canonical JSON text", scalar.Name, scalar.Tokens.Symbol)
		}
	}

	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(tempRoot, service)
	if err := SetReplacePaths(output, testpaths.Local(t), outDir); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}
	const runtimeTest = `package types

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestStructuredScalars(t *testing.T) {
	vec, err := ParseEmbeddingVector("[0.5, 1]")
	if err != nil || !reflect.DeepEqual(vec, EmbeddingVector{0.5, 1}) {
		t.Fatalf("ParseEmbeddingVector = %v, %v", vec, err)
	}
	if _, err := ParseEmbeddingVector("not a vector"); err == nil {
		t.Fatal("ParseEmbeddingVector accepted text that is not a float array")
	}
	tags, err := ParseGenericStringMap("{\"k\": \"v\"}")
	if err != nil || !reflect.DeepEqual(tags, GenericStringMap{"k": "v"}) {
		t.Fatalf("ParseGenericStringMap = %v, %v", tags, err)
	}

	var value Structured
	payload := []byte("{\"tags\": {}, \"optTags\": {\"k\": \"v\"}, \"vec\": [], \"vecs\": [[0.5], []]}")
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if errs := value.Validate(); errs.HasErrors() {
		t.Fatalf("an empty object and array are values: %v", errs)
	}
	encoded, err := json.Marshal(&value)
	if err != nil || string(encoded) != "{\"tags\":{},\"optTags\":{\"k\":\"v\"},\"vec\":[],\"vecs\":[[0.5],[]]}" {
		t.Fatalf("encode = %s, %v", encoded, err)
	}
	for _, text := range []string{"{\"tags\": \"{}\", \"vec\": []}", "{\"tags\": {}, \"vec\": \"[]\"}"} {
		var refused Structured
		if err := json.Unmarshal([]byte(text), &refused); err == nil {
			t.Fatalf("the Go type took JSON text: %s", text)
		}
	}
	var missing Structured
	if err := json.Unmarshal([]byte("{\"tags\": null}"), &missing); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errs := missing.Validate()
	for _, field := range []string{"tags", "vec"} {
		if got := errs.GetFieldErrors(field); len(got) != 1 || got[0].Validator != "required" {
			t.Fatalf("%s: %v, want required", field, got)
		}
	}
}
`
	if err := os.WriteFile(filepath.Join(outDir, "structured_runtime_test.go"), []byte(runtimeTest), 0o644); err != nil {
		t.Fatal(err)
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = outDir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
	}
	run := exec.Command("go", "test", "./...")
	run.Dir = outDir
	if out, err := run.CombinedOutput(); err != nil {
		t.Errorf("generated structured scalar module failed: %v\n%s", err, out)
	}
}
