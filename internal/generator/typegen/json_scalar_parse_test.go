package typegen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// jsonScalarSchema declares Generic.StringMap as a custom-parse, JSON-shaped
// scalar. The flag is set here rather than read from the scalar catalog so
// the test pins the generator path: a custom parser that returns canonical
// JSON text must be decoded into the map alias, not converted from a string.
func jsonScalarSchema() *ir.Schema {
	schema := ir.NewSchema("string-map-fixture", ir.SchemaKindGeneral)
	schema.Scalars["Generic.StringMap"] = &ir.ScalarDef{
		Name:              "Generic.StringMap",
		LanguagePrimitive: ir.LanguageString,
		HasCustomParse:    true,
		TypeMappings:      map[string]string{"go": "map[string]string", "json_schema": "object"},
	}
	return schema
}

func TestJSONScalarParserImports(t *testing.T) {
	schema := jsonScalarSchema()
	schema.Scalars["Generic.Int64"] = &ir.ScalarDef{
		Name:              "Generic.Int64",
		LanguagePrimitive: ir.LanguageNumber,
		HasCustomParse:    true,
		TypeMappings:      map[string]string{"go": "int64"},
	}
	output, err := Generate(schema, Options{
		SchemaName: schema.Name,
		ModulePath: "example.com/schemas/types/go/string-map-fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !output.HasJSONScalarParsers || !output.HasInt64ScalarParsers {
		t.Fatalf("want JSON and int64 scalar parsers, got json=%v int64=%v",
			output.HasJSONScalarParsers, output.HasInt64ScalarParsers)
	}
	for _, scalar := range output.Scalars {
		if scalar.Name == "Generic.StringMap" && (!scalar.ParseAsJSON || scalar.ParseTarget != "ParseGenericStringMap") {
			t.Fatalf("StringMap parse = (json %v, target %q), want (true, ParseGenericStringMap)",
				scalar.ParseAsJSON, scalar.ParseTarget)
		}
	}

	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(outDir, "scalars.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"encoding/json"`, `"strconv"`, "json.Unmarshal([]byte(canonical), &parsed)"} {
		if !strings.Contains(string(source), want) {
			t.Fatalf("scalars.go missing %q:\n%s", want, source)
		}
	}
}

// TestJSONScalarParserRuntime generates the module, drops a test into it that
// calls the generated ParseGenericStringMap against the real superscalar
// parser, and runs it.
func TestJSONScalarParserRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module test run in -short mode")
	}
	paths := testpaths.Local(t)

	output, err := Generate(jsonScalarSchema(), Options{
		SchemaName: "string-map-fixture",
		ModulePath: "example.com/schemas/types/go/string-map-fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(t.TempDir(), "string-map-fixture")
	if err := SetReplacePaths(output, paths, outDir); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}

	runtimeTest := fmt.Sprintf(`package %s

import "testing"

func TestStringMapParse(t *testing.T) {
	parsed, err := ParseGenericStringMap(%q)
	if err != nil || len(parsed) != 2 || parsed["traceparent"] != "00-example" || parsed["tracestate"] != "vendor=value" {
		t.Fatalf("valid map: %%v %%v", parsed, err)
	}
	empty, err := ParseGenericStringMap("{}")
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty map: %%v %%v", empty, err)
	}
	for _, invalid := range []string{%q, %q, %q, "not json"} {
		if _, err := ParseGenericStringMap(invalid); err == nil {
			t.Fatalf("invalid map accepted: %%s", invalid)
		}
	}
}
`, output.PackageName,
		`{"tracestate":"vendor=value","traceparent":"00-example"}`,
		`{"traceparent":null}`, `{"traceparent":1}`, `["not-a-map"]`)
	if err := os.WriteFile(filepath.Join(outDir, "string_map_runtime_test.go"), []byte(runtimeTest), 0o644); err != nil {
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
		t.Errorf("generated StringMap parser test failed: %v\n%s", err, out)
	}
}
