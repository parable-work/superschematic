package tsgen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// loadScalarService writes a data-form General service that declares the
// named catalog scalars and, when fields are given, one EmbeddedStruct named
// typeName with them, and loads it so every scalar is hydrated from the
// catalog as a real schema's is.
func loadScalarService(t *testing.T, name string, scalarNames []string, typeName string, fields []map[string]any) *ir.Schema {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	scalarDefs := map[string]any{}
	for _, scalar := range scalarNames {
		scalarDefs[scalar] = map[string]any{"name": scalar, "languagePrimitive": "string"}
	}
	source := map[string]any{"scalars": scalarDefs}
	if len(fields) > 0 {
		source["types"] = map[string]any{
			typeName: map[string]any{"name": typeName, "role": "EmbeddedStruct", "fields": fields},
		}
	}
	files := map[string]any{
		"schema.config.json": map[string]any{
			"name":    name,
			"kind":    "General",
			"outputs": map[string]any{"types": map[string]any{"typescript": map[string]any{"enabled": true}}},
		},
		filepath.Join("src", "fixture.schema.json"): source,
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
		t.Fatalf("load %s: %v", name, err)
	}
	return schema
}

// TestCatalogJSONParserScope pins which catalog scalars get the JSON parse
// adapter: the custom-parse scalars with a JSON shape, which in the linked
// catalog is Generic.StringMap alone.
func TestCatalogJSONParserScope(t *testing.T) {
	schema := loadScalarService(t, "catalog-fixture", registry.CoreScalars().Names(), "", nil)
	output, err := Generate(schema, Options{SchemaName: schema.Name})
	if err != nil {
		t.Fatal(err)
	}
	var adapted []string
	for _, scalar := range output.Scalars {
		if scalar.HasJSONParse {
			adapted = append(adapted, scalar.Name)
		}
	}
	if !slices.Equal(adapted, []string{"Generic.StringMap"}) {
		t.Fatalf("JSON parse adapters = %v, want [Generic.StringMap]", adapted)
	}
}

// TestGeneratedStringMapUsesCanonicalParser builds a types package with
// required and optional Generic.StringMap fields against the real
// superscalar binding, type-checks it, and runs a script that parses and
// validates maps through the generated functions.
func TestGeneratedStringMapUsesCanonicalParser(t *testing.T) {
	schema := loadScalarService(t, "string-map-fixture", []string{"Generic.StringMap"}, "StringMapFixture", []map[string]any{
		{"name": "values", "typeRef": map[string]any{"name": "Generic.StringMap"}, "required": true},
		{"name": "optional_values", "typeRef": map[string]any{"name": "Generic.StringMap"}},
	})
	output, err := Generate(schema, Options{SchemaName: schema.Name})
	if err != nil {
		t.Fatal(err)
	}
	for _, scalar := range output.Scalars {
		if scalar.Name == "Generic.StringMap" && scalar.TSType != "Record<string, string>" {
			t.Fatalf("Generic.StringMap TypeScript type = %q, want Record<string, string>", scalar.TSType)
		}
	}
	const runtimeTest = `
import type { StringMapFixture } from './types';
import { parseGenericStringMap, validateGenericStringMap, validateGenericStringMapRequired } from './validators/scalars/generic_string_map';
import { validateStringMapFixture } from './validators/types/stringmapfixture';

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

const valid: StringMapFixture = { values: { region: 'eu', tier: 'gold' }, optional_values: null };
const parsed: Record<string, string> | null = parseGenericStringMap(valid.values);
assert(parsed !== null && typeof parsed === 'object' && parsed.region === 'eu', 'the parser must return the map');
const fromJSON = parseGenericStringMap(JSON.stringify(valid.values));
assert(fromJSON !== null && fromJSON.region === 'eu' && fromJSON.tier === 'gold', 'JSON text must parse to the same map');
assert(validateStringMapFixture(valid) === true, 'the type validator rejected a valid map');
assert(validateGenericStringMap(null)[0], 'an optional null is valid');
assert(!validateGenericStringMapRequired(null)[0], 'a required null is invalid');
assert(validateGenericStringMapRequired({})[0], 'an empty map is valid');

// @ts-expect-error StringMap values are strings.
const invalidTyped: StringMapFixture = { values: { region: 1 }, optional_values: null };
assert(validateStringMapFixture(invalidTyped) !== true, 'the type validator accepted a numeric map value');
for (const invalid of [{ region: null }, { region: 1 }, ['eu']]) {
  assert(parseGenericStringMap(invalid) === null, 'the parser accepted an invalid map');
  const value = { values: invalid, optional_values: null } as unknown as StringMapFixture;
  assert(validateStringMapFixture(value) !== true, 'the type validator bypassed the map parser');
}
`
	runGeneratedPackageScript(t, output, runtimeTest)
}

// runGeneratedPackageScript writes output as a types package wired against
// the real superscalar binding, adds script as runtime_test.ts, then
// type-checks the package and runs the script with bun.
func runGeneratedPackageScript(t *testing.T, output *ModuleOutput, script string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping generated TypeScript runtime check in -short mode")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		requireOrSkipTSTooling(t, "bun unavailable")
	}
	paths := testpaths.Local(t)
	outDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := SetScalarLibSpec(output, paths, outDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "runtime_test.ts"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	install := exec.Command(bun, "install")
	install.Dir = outDir
	if result, err := install.CombinedOutput(); err != nil {
		requireOrSkipTSTooling(t, "bun install failed (likely offline): "+err.Error()+"\n"+string(result))
	}
	for _, args := range [][]string{{"x", "tsc", "--noEmit"}, {"run", "runtime_test.ts"}} {
		command := exec.Command(bun, args...)
		command.Dir = outDir
		if result, err := command.CombinedOutput(); err != nil {
			t.Fatalf("generated package check %v failed: %v\n%s", args, err, result)
		}
	}
}
