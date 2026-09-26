package pygen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// loadStringMapService loads a data-form General service with a required
// Generic.StringMap field and a required Generic.JSON field, hydrated from
// the catalog as a real schema's scalars are.
func loadStringMapService(t *testing.T) *ir.Schema {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "string-map-fixture")
	source := map[string]any{
		"scalars": map[string]any{
			"Generic.StringMap": map[string]any{"name": "Generic.StringMap", "languagePrimitive": "string"},
			"Generic.JSON":      map[string]any{"name": "Generic.JSON", "languagePrimitive": "string"},
		},
		"types": map[string]any{
			"StringMapFixture": map[string]any{
				"name": "StringMapFixture",
				"role": "EmbeddedStruct",
				"fields": []any{
					map[string]any{"name": "values", "typeRef": map[string]any{"name": "Generic.StringMap"}, "required": true},
					map[string]any{"name": "payload", "typeRef": map[string]any{"name": "Generic.JSON"}, "required": true},
				},
			},
		},
	}
	files := map[string]any{
		"schema.config.json": map[string]any{
			"name":    "string-map-fixture",
			"kind":    "General",
			"outputs": map[string]any{"types": map[string]any{"python": map[string]any{"enabled": true}}},
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
		t.Fatalf("load string-map-fixture: %v", err)
	}
	return schema
}

// TestGeneratedStringMapUsesCanonicalParser pins the Generic.StringMap
// binding: a Dict[str, str] field whose before-validator sends the value
// through superscalar's parser and keeps the decoded map. When python3 has
// pydantic and the superscalar package, it also runs the generated model.
func TestGeneratedStringMapUsesCanonicalParser(t *testing.T) {
	output, err := Generate(loadStringMapService(t), Options{SchemaName: "string-map-fixture"})
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
		"GenericStringMap = Annotated[\n    Dict[str, str],",
		"BeforeValidator(_custom_parse_generic_string_map)",
		"v = json.loads(v)",
		"parse_generic_string_map(json.dumps(v))",
		"return json.loads(parsed)",
	} {
		if !strings.Contains(string(source), expected) {
			t.Fatalf("generated StringMap binding is missing %q:\n%s", expected, source)
		}
	}

	if testing.Short() {
		t.Skip("skipping generated-package run in -short mode")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable; generated binding assertions passed")
	}
	if err := exec.Command(python, "-c", "import pydantic; from superscalar import parse_generic_string_map").Run(); err != nil {
		t.Skip("pydantic or the superscalar Python package unavailable; generated binding assertions passed")
	}
	command := exec.Command(python, "-B", "-c", `
import importlib
import sys
from unittest.mock import patch
from pydantic import ValidationError

module = importlib.import_module(sys.argv[1])
scalars = importlib.import_module(sys.argv[1] + ".scalars")
StringMapFixture = module.StringMapFixture

with patch.object(scalars, "parse_generic_string_map", wraps=scalars.parse_generic_string_map) as parser:
    model = StringMapFixture(values={"tier": "gold", "region": "eu"}, payload=[1, None])
    assert parser.call_count == 1, "the model bypassed the scalar parser"
assert model.values == {"region": "eu", "tier": "gold"}
assert isinstance(model.values, dict), "a StringMap stays a map, not JSON text"
assert model.model_dump(mode="json")["values"] == model.values
assert StringMapFixture(values='{"region":"eu"}', payload=1).values == {"region": "eu"}
schema = StringMapFixture.model_json_schema()["properties"]["values"]
assert schema["type"] == "object"
assert schema["additionalProperties"] == {"type": "string"}
for invalid in ({"region": None}, {"region": 1}, ["eu"]):
    try:
        StringMapFixture(values=invalid, payload=123)
    except ValidationError as error:
        assert error.errors()[0]["loc"] == ("values",)
    else:
        raise AssertionError("an invalid string map was accepted")
for arbitrary_json in ([1, None], "text", 123, True):
    assert StringMapFixture(values={}, payload=arbitrary_json).payload == arbitrary_json
`, output.PythonModuleName)
	command.Env = append(os.Environ(), "PYTHONPATH="+outDir+string(os.PathListSeparator)+os.Getenv("PYTHONPATH"))
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated StringMap model failed: %v\n%s", err, result)
	}
}

// TestGeneratedObjectParserUsesJSONTraits pins that the JSON parse wrapper
// follows the scalar's traits (custom parse with a JSON shape), not the
// Generic.StringMap name.
func TestGeneratedObjectParserUsesJSONTraits(t *testing.T) {
	schema := ir.NewSchema("object-parser-fixture", ir.SchemaKindGeneral)
	schema.Scalars["Acme.Headers"] = &ir.ScalarDef{
		Name:              "Acme.Headers",
		LanguagePrimitive: ir.LanguageObject,
		HasCustomParse:    true,
		TypeMappings: map[string]string{
			"python":      "Dict[str, str]",
			"json_schema": "object",
		},
	}
	output, err := Generate(schema, Options{SchemaName: schema.Name})
	if err != nil {
		t.Fatal(err)
	}
	if !output.HasJSONParse {
		t.Fatal("an object-shaped custom parser must enable the JSON parse wrapper")
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
		"import json",
		"v = json.loads(v)",
		"parse_acme_headers(json.dumps(v))",
		"return json.loads(parsed)",
	} {
		if !strings.Contains(string(source), expected) {
			t.Fatalf("generated object parser is missing %q:\n%s", expected, source)
		}
	}
}

// TestGeneratedEmbeddingVectorIsAList pins the Embedding.Vector binding: a
// JSON array scalar is a list[float] whose before-validator reads a string
// as the list's JSON text, whether or not superscalar is installed, and
// refuses any other JSON type. When python3 has pydantic, the generated
// model runs.
func TestGeneratedEmbeddingVectorIsAList(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vector-fixture")
	files := map[string]any{
		"schema.config.json": map[string]any{
			"name":    "vector-fixture",
			"kind":    "General",
			"outputs": map[string]any{"types": map[string]any{"python": map[string]any{"enabled": true}}},
		},
		filepath.Join("src", "fixture.schema.json"): map[string]any{
			"scalars": map[string]any{
				"Embedding.Vector": map[string]any{"name": "Embedding.Vector", "languagePrimitive": "string"},
			},
			"types": map[string]any{
				"VectorFixture": map[string]any{
					"name": "VectorFixture",
					"role": "EmbeddedStruct",
					"fields": []any{
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
		t.Fatalf("load vector-fixture: %v", err)
	}
	output, err := Generate(schema, Options{SchemaName: "vector-fixture"})
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
		"EmbeddingVector = Annotated[\n    list[float],",
		"BeforeValidator(_custom_parse_embedding_vector)",
		"parse_embedding_vector(json.dumps(v))",
	} {
		if !strings.Contains(string(source), expected) {
			t.Fatalf("generated Embedding.Vector binding is missing %q:\n%s", expected, source)
		}
	}

	if testing.Short() {
		t.Skip("skipping generated-package run in -short mode")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable; generated binding assertions passed")
	}
	if err := exec.Command(python, "-c", "import pydantic").Run(); err != nil {
		t.Skip("pydantic unavailable; generated binding assertions passed")
	}
	command := exec.Command(python, "-B", "-c", `
import importlib
import sys
from pydantic import ValidationError

VectorFixture = importlib.import_module(sys.argv[1]).VectorFixture
assert VectorFixture.model_validate({"vec": [0.5, 1]}, strict=True).vec == [0.5, 1.0]
assert VectorFixture.model_validate({"vec": "[0.5, 1]", "vecs": ["[]", [2]]}, strict=True).vecs == [[], [2.0]]
assert VectorFixture.model_validate({"vec": []}, strict=True).model_dump(mode="json")["vec"] == []
for invalid in ({"k": 1}, 1.5, True, ["a"], "not json", "{}", None):
    try:
        VectorFixture.model_validate({"vec": invalid}, strict=True)
    except ValidationError as error:
        assert error.errors()[0]["loc"][0] == "vec", error
    else:
        raise AssertionError(f"{invalid!r} was accepted as a vector")
`, output.PythonModuleName)
	command.Env = append(os.Environ(), "PYTHONPATH="+outDir+string(os.PathListSeparator)+os.Getenv("PYTHONPATH"))
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated Embedding.Vector model failed: %v\n%s", err, result)
	}
}
