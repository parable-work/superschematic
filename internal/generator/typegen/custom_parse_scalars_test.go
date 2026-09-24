package typegen

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

const customParseService = "custom-parse-scalars"

// fallbackParseExamples supplies parse inputs for custom-parse scalars
// whose superscalar metadata carries no example.
var fallbackParseExamples = map[string][]string{
	"Generic.StringMap": {`{"region":"eu","tier":"gold"}`},
}

// customParseScalars returns the canonical names of every scalar the linked
// catalog marks HasCustomParse, sorted. The list comes from the catalog so a
// scalar added to it is covered without editing this test.
func customParseScalars(t *testing.T) []string {
	t.Helper()
	catalog := registry.CoreScalars()
	var names []string
	for _, name := range catalog.Names() {
		if meta, ok := catalog.Scalar(name); ok && meta.HasCustomParse {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		t.Fatal("the linked scalar catalog marks no scalar HasCustomParse")
	}
	return names
}

// loadCustomParseService writes a data-form General service whose one type
// has a required field for each named scalar, then loads it through the
// loader so the scalars are hydrated from the catalog exactly as a real
// schema's are.
func loadCustomParseService(t *testing.T, names []string) *ir.Schema {
	t.Helper()
	dir := filepath.Join(t.TempDir(), customParseService)
	catalog := registry.CoreScalars()
	scalarDefs := map[string]any{}
	fields := make([]any, 0, len(names))
	for _, name := range names {
		meta, _ := catalog.Scalar(name)
		scalarDefs[name] = map[string]any{"name": name, "languagePrimitive": languagePrimitive(meta.Primitive)}
		symbol := codegen.BuildScalarTokens(name).Symbol
		fields = append(fields, map[string]any{
			"name":     strings.ToLower(symbol[:1]) + symbol[1:],
			"typeRef":  map[string]any{"name": name},
			"required": true,
		})
	}
	files := map[string]any{
		"schema.config.json": map[string]any{
			"name":    customParseService,
			"kind":    "General",
			"outputs": map[string]any{"types": map[string]any{"go": map[string]any{"enabled": true}}},
		},
		filepath.Join("src", "scalars.schema.json"): map[string]any{
			"scalars": scalarDefs,
			"types": map[string]any{
				"CustomParseScalars": map[string]any{
					"name":   "CustomParseScalars",
					"role":   "EmbeddedStruct",
					"fields": fields,
				},
			},
		},
	}
	for rel, doc := range files {
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	schema, err := loader.LoadService(dir)
	if err != nil {
		t.Fatalf("load %s: %v", customParseService, err)
	}
	for _, name := range names {
		if def := schema.Scalars[name]; def == nil || !def.HasCustomParse {
			t.Fatalf("%s was not hydrated as a custom-parse scalar", name)
		}
	}
	return schema
}

// languagePrimitive maps a catalog primitive onto the data-form
// languagePrimitive enum. The loader re-derives it from the catalog when it
// hydrates the scalar; the file only has to be valid.
func languagePrimitive(catalogPrimitive string) string {
	switch catalogPrimitive {
	case "Int", "Float":
		return "number"
	case "Bool":
		return "boolean"
	case "Object":
		return "object"
	default:
		return "string"
	}
}

func generateCustomParseModule(t *testing.T, names []string) *ModuleOutput {
	t.Helper()
	output, err := Generate(loadCustomParseService(t, names), Options{
		SchemaName: customParseService,
		ModulePath: "example.com/schemas/types/go/" + customParseService,
		Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for _, scalar := range output.Scalars {
		if scalar.ParseTarget == "" {
			t.Errorf("%s: no Parse%s is emitted for a custom-parse scalar", scalar.Name, scalar.Tokens.Symbol)
		}
	}
	return output
}

// TestCustomParseScalarsGolden pins the generated module for a schema that
// uses every custom-parse scalar in the catalog. Regenerate with:
// go test ./internal/generator/typegen -run TestCustomParseScalarsGolden -update
func TestCustomParseScalarsGolden(t *testing.T) {
	output := generateCustomParseModule(t, customParseScalars(t))

	tempRoot := t.TempDir()
	outDir := filepath.Join(tempRoot, customParseService)
	paths := naming.LocalPaths{ScalarGo: filepath.Join(tempRoot, "scalars", "go"), SchemaIR: filepath.Join(tempRoot, "ir")}
	if err := SetReplacePaths(output, paths, outDir); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	compareWithGolden(t, outDir, filepath.Join("testdata", "golden", customParseService))
}

// TestCustomParseScalarsCompile generates the same module wired against the
// real superscalar Go binding, builds it, and runs a test inside it that
// feeds each scalar's catalog examples through the generated Parse<Symbol>
// and validates a record holding the parsed values. A Parse<Symbol> bound to
// a function the binding does not export fails the build here.
func TestCustomParseScalarsCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module build in -short mode")
	}
	paths := testpaths.Local(t)
	output := generateCustomParseModule(t, customParseScalars(t))

	outDir := filepath.Join(t.TempDir(), customParseService)
	if err := SetReplacePaths(output, paths, outDir); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}

	var record *TypeInfo
	for i := range output.Types {
		if output.Types[i].Name == "CustomParseScalars" {
			record = &output.Types[i]
		}
	}
	if record == nil {
		t.Fatal("CustomParseScalars was not generated")
	}
	catalog := registry.CoreScalars()
	var body strings.Builder
	for _, field := range record.Fields {
		scalar := field.ScalarInfo
		meta, _ := catalog.Scalar(scalar.Name)
		examples := meta.Examples
		if len(examples) == 0 {
			examples = fallbackParseExamples[scalar.Name]
		}
		if len(examples) == 0 {
			t.Fatalf("%s has no catalog example to parse; add one to fallbackParseExamples", scalar.Name)
		}
		for i, example := range examples {
			target := "_"
			if i == 0 {
				target = "record." + field.GoName
			}
			fmt.Fprintf(&body, "\tif %s, err = Parse%s(%q); err != nil {\n\t\tt.Errorf(\"%s: catalog example %%q rejected: %%v\", %q, err)\n\t}\n",
				target, scalar.Tokens.Symbol, example, scalar.Name, example)
		}
	}
	runtimeTest := fmt.Sprintf(`package %s

import "testing"

func TestCustomParseScalarExamples(t *testing.T) {
	var record CustomParseScalars
	var err error
%s	if errs := record.Validate(); errs.HasErrors() {
		t.Errorf("record of parsed catalog examples fails validation: %%v", errs)
	}
}
`, output.PackageName, body.String())
	if err := os.WriteFile(filepath.Join(outDir, "custom_parse_runtime_test.go"), []byte(runtimeTest), 0o644); err != nil {
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
		t.Errorf("generated custom-parse scalar module failed: %v\n%s", err, out)
	}
}
