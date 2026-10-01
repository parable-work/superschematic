package pygen

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestGeneratedOptionalGenericJSONKeepsNull generates a model with an
// optional Generic.JSON field and runs a probe: the model records a field
// given as None apart from one it was not given, and a dump that leaves out
// None (exclude_none, as the SDK sends a model) still writes the first as
// null, under its wire name or its Python name, in a nested model too, and
// not when the dump excludes it. The probe needs pydantic and skips without
// it.
func TestGeneratedOptionalGenericJSONKeepsNull(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available; skipping the Python probe")
	}
	if err := exec.Command(pythonPath, "-c", "import pydantic").Run(); err != nil {
		t.Skip("pydantic not available; skipping the Python probe")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, encoded := range []string{
		`{"name":"Note","role":"APIInput","fields":[
			{"name":"body","typeRef":{"name":"Generic.JSON"},"required":true},
			{"name":"extraNote","typeRef":{"name":"Generic.JSON"}},
			{"name":"label","typeRef":{"name":"string"}}
		]}`,
		`{"name":"Holder","role":"EmbeddedStruct","fields":[
			{"name":"notes","typeRef":{"name":"Note","isArray":true},"required":true}
		]}`,
	} {
		var model ir.TypeDef
		if err := json.Unmarshal([]byte(encoded), &model); err != nil {
			t.Fatal(err)
		}
		schema.Types[model.Name] = &model
	}
	output, err := Generate(schema, Options{SchemaName: "optional-json"})
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(t.TempDir(), "optional-json")
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}
	probe := exec.Command(pythonPath, "-c", fmt.Sprintf(optionalJSONProbe, outDir, output.PythonModuleName))
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("optional Generic.JSON probe failed: %v\n%s", err, out)
	}
}

// optionalJSONProbe is formatted with the package directory and module.
const optionalJSONProbe = `
import sys

sys.path.insert(0, %[1]q)
types = __import__(%[2]q + ".types", fromlist=["Note", "Holder"])
Note, Holder = types.Note, types.Holder

def wire(model, **options):
    return model.model_dump(by_alias=True, mode="json", exclude_none=True, **options)

absent = Note.model_validate({"body": 1})
nulled = Note.model_validate({"body": 1, "extraNote": None, "label": None})
assert "extra_note" not in absent.model_fields_set and "extra_note" in nulled.model_fields_set
assert wire(absent) == {"body": 1}, wire(absent)
# A null optional Generic.JSON is written; a null optional string is not.
assert wire(nulled) == {"body": 1, "extraNote": None}, wire(nulled)
assert nulled.model_dump(exclude_none=True) == {"body": 1, "extra_note": None}, nulled.model_dump(exclude_none=True)
assert wire(Note(body=1, extra_note=None)) == {"body": 1, "extraNote": None}
assert wire(Note(body=1, extra_note={"a": None})) == {"body": 1, "extraNote": {"a": None}}
assert wire(nulled, exclude={"extra_note"}) == {"body": 1}, wire(nulled, exclude={"extra_note"})
assert not nulled.validate_all().errors, nulled.validate_all().errors
holder = Holder.model_validate({"notes": [{"body": 1, "extraNote": None}, {"body": 2}]})
assert wire(holder) == {"notes": [{"body": 1, "extraNote": None}, {"body": 2}]}, wire(holder)
`
