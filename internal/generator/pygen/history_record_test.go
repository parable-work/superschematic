package pygen

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// recipesSchema declares a versioned table (Recipe) next to a table that is
// not versioned (Step).
func recipesSchema() *ir.Schema {
	schema := ir.NewSchema("recipes", ir.SchemaKindDB)
	schema.Types["Recipe"] = &ir.TypeDef{Name: "Recipe", Role: ir.RoleDBTable, Versioned: true, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		{Name: "notes", TypeRef: ir.TypeRef{Name: "string"}},
		{Name: "vegetarian", TypeRef: ir.TypeRef{Name: "boolean"}, Required: true},
	}}
	schema.Types["Step"] = &ir.TypeDef{Name: "Step", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
	}}
	return schema
}

// goHistoryRecord and goHistoryRecordNanos are what json.Marshal writes for
// the Go HistoryRecord[Recipe] with a microsecond and a nanosecond
// recordedAt: fields in declaration order, an absent optional field left
// out.
const (
	goHistoryRecord      = `{"version":3,"operation":"UPDATE","recordedAt":"2026-01-02T03:04:05.123456Z","value":{"id":"r1","name":"Soup","vegetarian":true,"_version":3}}`
	goHistoryRecordNanos = `{"version":3,"operation":"UPDATE","recordedAt":"2026-01-02T03:04:05.123456789Z","value":{"id":"r1","name":"Soup","vegetarian":true,"_version":3}}`
)

// TestGeneratedHistoryRecord runs the generated package: HistoryRecord[Recipe]
// reads the JSON Go writes, and writes it back with the same wire names and
// values (an unset optional field as null). Python's datetime holds
// microseconds, so a nanosecond recordedAt reads truncated. A value without
// _version reads as version 0, as Go decodes it; a table that is not
// versioned has no version field.
func TestGeneratedHistoryRecord(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated history record check in -short mode")
	}
	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available; skipping generated history record check")
	}
	if err := exec.Command(pythonPath, "-c", "import pydantic").Run(); err != nil {
		t.Skip("pydantic not available; skipping generated history record check")
	}
	output, err := Generate(recipesSchema(), Options{SchemaName: "recipes"})
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(t.TempDir(), "recipes")
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	b.WriteString("import datetime\nimport json\nimport sys\n")
	b.WriteString("from pydantic import ValidationError\n")
	fmt.Fprintf(&b, "sys.path.insert(0, %q)\n", outDir)
	fmt.Fprintf(&b, "from %s import HistoryRecord, Recipe, Step\n", output.PythonModuleName)
	fmt.Fprintf(&b, "GO_RECORD = %q\n", goHistoryRecord)
	fmt.Fprintf(&b, "GO_RECORD_NANOS = %q\n", goHistoryRecordNanos)
	b.WriteString(`
record = HistoryRecord[Recipe].from_json(GO_RECORD)
assert record.version == 3, record
assert record.operation == "UPDATE", record
assert record.recorded_at == datetime.datetime(2026, 1, 2, 3, 4, 5, 123456, tzinfo=datetime.timezone.utc), record
assert isinstance(record.value, Recipe), record
assert record.value.name == "Soup" and record.value.notes is None, record
assert record.value.version_ == 3, record
want = json.loads(GO_RECORD)
want["value"]["notes"] = None
assert record.to_json_dict() == want, record.to_json_dict()
assert json.loads(record.to_json()) == want, record.to_json()
assert HistoryRecord[Recipe].from_json(record.to_json()) == record

nanos = HistoryRecord[Recipe].from_json(GO_RECORD_NANOS)
assert nanos.recorded_at == record.recorded_at, nanos

for key in ["version", "operation", "recordedAt", "value"]:
    payload = json.loads(GO_RECORD)
    del payload[key]
    try:
        HistoryRecord[Recipe].from_dict(payload)
    except ValidationError:
        pass
    else:
        raise AssertionError(f"record without {key} was accepted")

recipe = Recipe.from_json('{"id":"r1","name":"Soup","vegetarian":false}')
assert recipe.version_ == 0, recipe
assert Recipe.from_json(json.dumps(want["value"])).version_ == 3

assert "version_" not in Step.model_fields
assert "_version" not in Step(id="s1", title="Chop").model_dump(by_alias=True)
`)
	cmd := exec.Command(pythonPath, "-c", b.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated Python history record check: %v\n%s", err, out)
	}
}
