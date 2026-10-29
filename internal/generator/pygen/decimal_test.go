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

// TestGeneratedDecimalIsADecimal pins the Generic.Decimal binding (D84): a
// field of it is a decimal.Decimal that superscalar reads from its text or
// from a decimal.Decimal into the canonical value, that refuses a float and
// what the scalar refuses, and that a JSON dump writes as the canonical
// text; validate_all checks Validate's precision and scale on it. The model
// runs in runtime/schema/python's environment, which has superscalar and
// pydantic.
func TestGeneratedDecimalIsADecimal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "decimal-fixture")
	files := map[string]any{
		"schema.config.json": map[string]any{
			"name":    "decimal-fixture",
			"kind":    "General",
			"outputs": map[string]any{"types": map[string]any{"python": map[string]any{"enabled": true}}},
		},
		filepath.Join("src", "fixture.schema.json"): map[string]any{
			"scalars": map[string]any{
				"Generic.Decimal":     map[string]any{"name": "Generic.Decimal", "languagePrimitive": "string"},
				"Finance.SignedMoney": map[string]any{"name": "Finance.SignedMoney", "languagePrimitive": "number"},
			},
			"types": map[string]any{
				"Rate": map[string]any{
					"name": "Rate",
					"role": "EmbeddedStruct",
					"fields": []any{
						map[string]any{"name": "rate", "typeRef": map[string]any{"name": "Generic.Decimal"}, "required": true, "validatePrecision": 5, "validateScale": 2},
						map[string]any{"name": "rates", "typeRef": map[string]any{"name": "Generic.Decimal", "isArray": true}},
						map[string]any{"name": "amount", "typeRef": map[string]any{"name": "Finance.SignedMoney"}},
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
		t.Fatalf("load decimal-fixture: %v", err)
	}
	output, err := Generate(schema, Options{SchemaName: "decimal-fixture"})
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
		"import decimal\n",
		"parse_generic_decimal_to_decimal,",
		"GenericDecimal = Annotated[\n    decimal.Decimal,",
		"PlainValidator(_parse_generic_decimal_value)",
		`PlainSerializer(_serialize_generic_decimal, return_type=str, when_used="json")`,
	} {
		if !strings.Contains(string(source), expected) {
			t.Fatalf("generated Generic.Decimal binding is missing %q:\n%s", expected, source)
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
	if out, err := exec.Command(uv, "run", "--project", project, "python", "-c", "import pydantic, superscalar").CombinedOutput(); err != nil {
		t.Skipf("runtime/schema/python's environment lacks pydantic or superscalar: %v\n%s", err, out)
	}
	command := exec.Command(uv, "run", "--project", project, "python", "-B", "-c", `
import decimal
import importlib
import json
import sys
from pydantic import ValidationError

Rate = importlib.import_module(sys.argv[1]).Rate
rate = Rate.model_validate({"rate": "001.50", "rates": ["-0.00", decimal.Decimal("1E-7")], "amount": -1250}, strict=True)
assert isinstance(rate.rate, decimal.Decimal) and rate.rate == decimal.Decimal("1.5"), rate.rate
assert str(rate.rate) == "1.5", str(rate.rate)
assert rate.rates == [decimal.Decimal("0"), decimal.Decimal("0.0000001")], rate.rates
assert rate.amount == -1250
dumped = json.loads(rate.to_json())
assert dumped["rate"] == "1.5" and dumped["rates"] == ["0", "0.0000001"], dumped
assert rate.validate_all().errors == {}, rate.validate_all().errors
for invalid in (1.5, "1e3", "NaN", "", None, decimal.Decimal("Infinity")):
    try:
        Rate.model_validate({"rate": invalid}, strict=True)
    except ValidationError as error:
        assert error.errors()[0]["loc"][0] == "rate", error
    else:
        raise AssertionError(f"{invalid!r} was accepted as a decimal")
too_many = Rate.model_validate({"rate": "1234.567"}, strict=True)
assert sorted(e["validator"] for e in too_many.validate_all().errors["rate"]) == ["precision", "scale"], too_many.validate_all().errors
`, output.PythonModuleName)
	command.Env = append(os.Environ(), "PYTHONPATH="+outDir+string(os.PathListSeparator)+os.Getenv("PYTHONPATH"))
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated Generic.Decimal model failed: %v\n%s", err, result)
	}
}
