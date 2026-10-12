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
// catalog are Generic.StringMap and Geo.Location.
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
	if !slices.Equal(adapted, []string{"Generic.StringMap", "Geo.Location"}) {
		t.Fatalf("JSON parse adapters = %v, want [Generic.StringMap Geo.Location]", adapted)
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

// TestGeneratedStructuredScalarsCheckTheirShape pins the validators of the
// JSON object and array scalars (Generic.StringMap, Embedding.Vector): the
// object or array is a value, and so is its JSON text; an empty string is
// no value; any other JSON type is "type"; superscalar checks what the
// value holds.
func TestGeneratedStructuredScalarsCheckTheirShape(t *testing.T) {
	schema := loadScalarService(t, "structured-fixture", []string{"Generic.StringMap", "Embedding.Vector"}, "StructuredFixture", []map[string]any{
		{"name": "tags", "typeRef": map[string]any{"name": "Generic.StringMap"}, "required": true},
		{"name": "vec", "typeRef": map[string]any{"name": "Embedding.Vector"}, "required": true},
		{"name": "vecs", "typeRef": map[string]any{"name": "Embedding.Vector", "isArray": true}},
	})
	output, err := Generate(schema, Options{SchemaName: schema.Name})
	if err != nil {
		t.Fatal(err)
	}
	for _, scalar := range output.Scalars {
		want := map[string]string{"Generic.StringMap": "object", "Embedding.Vector": "array"}[scalar.Name]
		if scalar.StructuredJSON != want || !scalar.UsesLibValidate {
			t.Fatalf("%s: StructuredJSON = %q, UsesLibValidate = %v; want %q, true", scalar.Name, scalar.StructuredJSON, scalar.UsesLibValidate, want)
		}
	}
	const runtimeTest = `
import type { StructuredFixture } from './types';
import { validateEmbeddingVector, validateEmbeddingVectorRequired } from './validators/scalars/embedding_vector';
import { validateGenericStringMap, validateGenericStringMapRequired } from './validators/scalars/generic_string_map';
import { validateStructuredFixture } from './validators/types/structuredfixture';

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

function validators(result: ReturnType<typeof validateEmbeddingVector>): string[] {
  return result[0] ? [] : (result[1] ?? []).map((e) => e.validator);
}

const valid: StructuredFixture = { tags: { k: 'v' }, vec: [0.5, 1], vecs: [[], [2]] };
assert(validateStructuredFixture(valid) === true, 'a map and vectors were refused');
for (const value of [[0.5, 1], [], '[0.5, 1]', '[]']) {
  assert(validateEmbeddingVectorRequired(value as never)[0], 'vector refused: ' + JSON.stringify(value));
}
for (const value of [{ k: 'v' }, {}, '{"k": "v"}']) {
  assert(validateGenericStringMapRequired(value as never)[0], 'map refused: ' + JSON.stringify(value));
}
for (const value of [{ k: 1 }, 1.5, true]) {
  assert(validators(validateEmbeddingVector(value as never)).join() === 'type', 'vector type: ' + JSON.stringify(value));
}
for (const value of [['k'], 42, false]) {
  assert(validators(validateGenericStringMap(value as never)).join() === 'type', 'map type: ' + JSON.stringify(value));
}
assert(!validateEmbeddingVector(['a'] as never)[0], 'superscalar did not check the elements');
assert(!validateGenericStringMap({ k: 1 } as never)[0], 'superscalar did not check the values');
assert(validators(validateEmbeddingVectorRequired('' as never)).join() === 'required', 'a required empty string is missing');
assert(validateEmbeddingVector('' as never)[0], 'an optional empty string is absent');
assert(validators(validateGenericStringMapRequired(null)).join() === 'required', 'a required null is missing');
const wrong = validateStructuredFixture({ tags: ['k'], vec: {}, vecs: [[1], 7, null] } as unknown as StructuredFixture);
assert(wrong !== true, 'wrong shapes were accepted');
assert(JSON.stringify(Object.keys(wrong).sort()) === JSON.stringify(['tags', 'vec', 'vecs[1]', 'vecs[2]']), JSON.stringify(wrong));
`
	runGeneratedPackageScript(t, output, runtimeTest)
}

// TestGeneratedGeoLocationIsAnObject pins Geo.Location on the structured
// object path: the {lat, lon} object or its JSON text is a value, the parser
// hands back the canonical object, a {0, 0} location is a value, and
// superscalar refuses what the object holds, each failure under the core's
// own name: the "lat,lon" string is parse, an unknown, missing or
// non-number member is custom, and an out-of-range degree is range.
func TestGeneratedGeoLocationIsAnObject(t *testing.T) {
	schema := loadScalarService(t, "geo-fixture", []string{"Geo.Location"}, "GeoFixture", []map[string]any{
		{"name": "at", "typeRef": map[string]any{"name": "Geo.Location"}, "required": true},
		{"name": "near", "typeRef": map[string]any{"name": "Geo.Location"}},
		{"name": "route", "typeRef": map[string]any{"name": "Geo.Location", "isArray": true}},
	})
	output, err := Generate(schema, Options{SchemaName: schema.Name})
	if err != nil {
		t.Fatal(err)
	}
	for _, scalar := range output.Scalars {
		if scalar.Name != "Geo.Location" {
			continue
		}
		if scalar.StructuredJSON != "object" || !scalar.UsesLibValidate || !scalar.HasJSONParse || scalar.TSType != "{ lat: number; lon: number }" {
			t.Fatalf("Geo.Location: StructuredJSON = %q, UsesLibValidate = %v, HasJSONParse = %v, TSType = %q", scalar.StructuredJSON, scalar.UsesLibValidate, scalar.HasJSONParse, scalar.TSType)
		}
	}
	const runtimeTest = `
import type { GeoFixture } from './types';
import { parseGeoLocation, validateGeoLocation, validateGeoLocationRequired } from './validators/scalars/geo_location';
import { validateGeoFixture } from './validators/types/geofixture';

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

function validators(result: ReturnType<typeof validateGeoLocation>): string[] {
  return result[0] ? [] : (result[1] ?? []).map((e) => e.validator);
}

const valid: GeoFixture = { at: { lat: 37.7749, lon: -122.4194 }, near: null, route: [{ lat: 0, lon: 0 }] };
assert(validateGeoFixture(valid) === true, 'a location was refused');
for (const value of [{ lat: 90, lon: -180 }, { lat: 0, lon: 0 }, '{"lon": -122.4194, "lat": 37.7749}']) {
  assert(validateGeoLocationRequired(value as never)[0], 'location refused: ' + JSON.stringify(value));
}
for (const value of [{ lon: -122.4194, lat: 37.7749 }, '{"lon": -122.4194, "lat": 37.7749}']) {
  const parsed = parseGeoLocation(value as never);
  assert(parsed !== null && JSON.stringify(parsed) === '{"lat":37.7749,"lon":-122.4194}', 'parse: ' + JSON.stringify(parsed));
}
const refused: Array<[unknown, string]> = [
  ['37.7749,-122.4194', 'parse'],
  [{ lat: 91, lon: 0 }, 'range'],
  [{ lat: 0, lon: -180.5 }, 'range'],
  [{ lat: 1, lon: 2, alt: 3 }, 'custom'],
  [{ lat: 1 }, 'custom'],
  [{ lat: '1', lon: 2 }, 'custom'],
  [[37.7749, -122.4194], 'type'],
  [42, 'type'],
];
for (const [value, name] of refused) {
  assert(validators(validateGeoLocation(value as never)).join() === name, JSON.stringify(value) + ' -> ' + JSON.stringify(validateGeoLocation(value as never)));
  assert(parseGeoLocation(value as never) === null, 'the parser accepted ' + JSON.stringify(value));
}
assert(validators(validateGeoLocationRequired(null)).join() === 'required', 'a required null is missing');
assert(validators(validateGeoLocationRequired('' as never)).join() === 'required', 'a required empty string is missing');
assert(validateGeoLocation(null)[0] && validateGeoLocation('' as never)[0], 'an optional null or empty string is absent');
const wrong = validateGeoFixture({ at: '37.7749,-122.4194', near: { lat: 1, lon: 2, alt: 3 }, route: [{ lat: 91, lon: 0 }] } as unknown as GeoFixture);
assert(wrong !== true, 'wrong locations were accepted');
assert(JSON.stringify(Object.keys(wrong).sort()) === JSON.stringify(['at', 'near', 'route[0]']), JSON.stringify(wrong));
`
	runGeneratedPackageScript(t, output, runtimeTest)
}
