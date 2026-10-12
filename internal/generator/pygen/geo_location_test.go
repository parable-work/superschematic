package pygen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestGeneratedGeoLocationIsATypedObject pins the Geo.Location binding: its
// value type is the scalar library's GeoLocation TypedDict, a plain dict
// when the library is absent, its example is the decoded object, and its
// before-validator sends the value through superscalar's parser rather than
// the old "lat,lon" string reader. With uv, the generated model runs in
// runtime/schema/python's environment, which has superscalar and pydantic.
func TestGeneratedGeoLocationIsATypedObject(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "geo-fixture")
	files := map[string]any{
		"schema.config.json": map[string]any{
			"name":    "geo-fixture",
			"kind":    "General",
			"outputs": map[string]any{"types": map[string]any{"python": map[string]any{"enabled": true}}},
		},
		filepath.Join("src", "fixture.schema.json"): map[string]any{
			"scalars": map[string]any{
				// The loader keeps a schema's own example; the catalog's is not
				// copied, so the fixture declares the object's JSON text.
				"Geo.Location": map[string]any{"name": "Geo.Location", "languagePrimitive": "string", "example": `{"lat":37.7749,"lon":-122.4194}`},
			},
			"types": map[string]any{
				"GeoFixture": map[string]any{
					"name": "GeoFixture",
					"role": "EmbeddedStruct",
					"fields": []any{
						map[string]any{"name": "at", "typeRef": map[string]any{"name": "Geo.Location"}, "required": true},
						map[string]any{"name": "near", "typeRef": map[string]any{"name": "Geo.Location"}},
						map[string]any{"name": "route", "typeRef": map[string]any{"name": "Geo.Location", "isArray": true}},
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
		t.Fatalf("load geo-fixture: %v", err)
	}
	output, err := Generate(schema, Options{SchemaName: "geo-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(outDir, output.PythonModuleName, "scalars.py"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"    from superscalar import GeoLocation as _GeoLocationValue\nexcept ImportError:\n    _GeoLocationValue = Dict[str, Any]",
		"GeoLocation = Annotated[\n    _GeoLocationValue,",
		`examples=[{"lat": 37.7749, "lon": -122.4194}]`,
		"BeforeValidator(_custom_parse_geo_location)",
		"parse_geo_location(json.dumps(v))",
	} {
		if !strings.Contains(string(source), expected) {
			t.Fatalf("generated Geo.Location binding is missing %q:\n%s", expected, source)
		}
	}
	for _, retired := range []string{"_validate_location", "'lat,lon' string"} {
		if strings.Contains(string(source), retired) {
			t.Fatalf("generated Geo.Location binding still has %q:\n%s", retired, source)
		}
	}

	if testing.Short() {
		t.Skip("skipping generated-package run in -short mode")
	}
	uv, err := exec.LookPath("uv")
	if err != nil {
		t.Skip("uv unavailable; generated binding assertions passed")
	}
	project := filepath.Join(testpaths.RepoRoot(t), "runtime", "schema", "python")
	run := func(script string) {
		t.Helper()
		command := exec.Command(uv, "run", "--project", project, "python", "-B", "-c", script, output.PythonModuleName)
		command.Env = append(os.Environ(), "PYTHONPATH="+outDir)
		if result, err := command.CombinedOutput(); err != nil {
			t.Fatalf("generated Geo.Location model failed: %v\n%s", err, result)
		}
	}
	run(`
import importlib
import sys
import superscalar
from pydantic import ValidationError

module = importlib.import_module(sys.argv[1])
scalars = importlib.import_module(sys.argv[1] + ".scalars")
GeoFixture = module.GeoFixture

assert scalars._GeoLocationValue is superscalar.GeoLocation
model = GeoFixture(at={"lon": -122.4194, "lat": 37.7749}, near=None, route=[{"lat": 0, "lon": 0}])
assert model.at == {"lat": 37.7749, "lon": -122.4194}, model.at
assert model.route == [{"lat": 0, "lon": 0}], "a {0, 0} location is a value"
assert GeoFixture(at='{"lon": 2, "lat": 1}').at == {"lat": 1, "lon": 2}
assert model.model_dump(mode="json")["at"] == {"lat": 37.7749, "lon": -122.4194}
schema = GeoFixture.model_json_schema()
location = schema["$defs"]["GeoLocation"] if "$defs" in schema and "GeoLocation" in schema["$defs"] else schema["properties"]["at"]
assert location["type"] == "object" and sorted(location["required"]) == ["lat", "lon"], location
for invalid in ("37.7749,-122.4194", {"lat": 91, "lon": 0}, {"lat": 1, "lon": 2, "alt": 3}, {"lat": 1}, {"lat": "1", "lon": 2}, [37.7749, -122.4194], 42):
    for field in ("at", "near"):
        try:
            GeoFixture(**{"at": {"lat": 0, "lon": 0}, field: invalid})
        except ValidationError as error:
            assert error.errors()[0]["loc"] == (field,), error.errors()
        else:
            raise AssertionError(f"{field} accepted {invalid!r}")
`)
	// Without superscalar the value is a plain dict: the JSON text still
	// parses, and nothing checks what the object holds.
	run(`
import importlib
import sys
from typing import Any, Dict

sys.modules["superscalar"] = None
scalars = importlib.import_module(sys.argv[1] + ".scalars")
assert scalars._GeoLocationValue == Dict[str, Any]
GeoFixture = importlib.import_module(sys.argv[1]).GeoFixture
assert GeoFixture(at='{"lat": 1, "lon": 2}').at == {"lat": 1, "lon": 2}
`)
}
