package pygen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// wireNamesSchema declares a type whose fields' wire names differ from
// their snake_case names (lineItems, shipNote) next to one that does not
// (note), and a type whose names all agree.
func wireNamesSchema() *ir.Schema {
	listMin, maxLength := 1, 4
	schema := ir.NewSchema("wire-names", ir.SchemaKindGeneral)
	schema.Types["Order"] = &ir.TypeDef{
		Name: "Order",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "lineItems", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, Required: true, ValidateListMin: &listMin, ValidateMaxLength: &maxLength},
			{Name: "note", TypeRef: ir.TypeRef{Name: "string"}, ValidateMaxLength: &maxLength},
			{Name: "shipNote", TypeRef: ir.TypeRef{Name: "string"}, ValidateMaxLength: &maxLength},
		},
	}
	schema.Types["Tag"] = &ir.TypeDef{
		Name: "Tag",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "label", TypeRef: ir.TypeRef{Name: "string"}, Required: true, ValidateMaxLength: &maxLength},
		},
	}
	return schema
}

func writeWireNamesTypes(t *testing.T) (string, *ModuleOutput) {
	t.Helper()
	output, err := Generate(wireNamesSchema(), Options{
		SchemaName: "wire-names",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	return outDir, output
}

// TestValidateAllByAliasRenders pins the rename validate_all(by_alias=True)
// applies: only the fields whose snake_case key differs from the wire name
// are listed, and a type with none has no rename.
func TestValidateAllByAliasRenders(t *testing.T) {
	outDir, output := writeWireNamesTypes(t)
	typesPy, err := os.ReadFile(filepath.Join(outDir, output.PythonModuleName, "types.py"))
	if err != nil {
		t.Fatalf("read types.py: %v", err)
	}
	src := string(typesPy)
	if got := strings.Count(src, "def validate_all(self, *, by_alias: bool = False) -> ValidationErrors:"); got != 2 {
		t.Errorf("validate_all(by_alias) signature rendered %d times, want 2", got)
	}
	want := `        if by_alias:
            return errors._with_field_names({
                "line_items": "lineItems",
                "ship_note": "shipNote",
            })

        return errors`
	if !strings.Contains(src, want) {
		t.Errorf("types.py missing the Order rename:\n%s\n--- types.py ---\n%s", want, src)
	}
	if got := strings.Count(src, "if by_alias:"); got != 1 {
		t.Errorf("types.py renders %d by_alias renames, want 1 (Tag's names all agree)", got)
	}
}

// TestValidateAllByAliasRun imports the generated wire-names package and
// checks at run time that validate_all keys errors by snake_case name and,
// with by_alias, by wire name, index suffixes kept; and that str() of a
// ValidationErrors renders the errors it holds, including ones added after
// it was built. Skips when python3 or pydantic is unavailable.
func TestValidateAllByAliasRun(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-package run in -short mode")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	if err := exec.Command(python, "-c", "import pydantic").Run(); err != nil {
		t.Skip("pydantic not available")
	}
	outDir, output := writeWireNamesTypes(t)

	command := exec.Command(python, "-B", "-c", `
import importlib
import sys

mod = importlib.import_module(sys.argv[1])
Order, Tag, ValidationErrors = mod.Order, mod.Tag, mod.ValidationErrors


def verdicts(errors):
    return {key: [e["validator"] for e in entries] for key, entries in errors.errors.items()}


empty = Order.model_validate({"lineItems": [], "note": "too long", "shipNote": "too long"}, strict=True)
assert verdicts(empty.validate_all()) == {
    "line_items": ["listMin"],
    "note": ["maxLength"],
    "ship_note": ["maxLength"],
}, verdicts(empty.validate_all())
assert verdicts(empty.validate_all(by_alias=True)) == {
    "lineItems": ["listMin"],
    "note": ["maxLength"],
    "shipNote": ["maxLength"],
}, verdicts(empty.validate_all(by_alias=True))

long_item = Order.model_validate({"lineItems": ["ok", "far too long"]}, strict=True)
assert verdicts(long_item.validate_all(by_alias=True)) == {"lineItems[1]": ["maxLength"]}
null_item = Order.model_construct(line_items=["ok", None])
assert verdicts(null_item.validate_all(by_alias=True)) == {"lineItems[1]": ["required"]}
assert verdicts(Tag.model_validate({"label": "too long"}).validate_all(by_alias=True)) == {"label": ["maxLength"]}

# str() renders the errors; "No validation errors" only when there are none.
errors = long_item.validate_all()
assert errors.has_errors()
assert str(errors) == "Validation failed:\n  line_items[1]: must be at most 4 characters (maxLength)", str(errors)
assert str(long_item.validate_all(by_alias=True)).splitlines()[1] == "  lineItems[1]: must be at most 4 characters (maxLength)"
assert str(Tag.model_validate({"label": "ok"}).validate_all()) == "No validation errors"
built = ValidationErrors()
assert str(built) == "No validation errors"
built.add_field_error("label", "required", "required field")
assert str(built) == "Validation failed:\n  label: required field (required)", str(built)
assert str(ValidationErrors({"label": [{"validator": "maxLength", "message": "too long"}]})) == "Validation failed:\n  label: too long (maxLength)"
`, output.PythonModuleName)
	command.Env = append(os.Environ(), "PYTHONPATH="+outDir)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated wire-names package run failed: %v\n%s", err, out)
	}
}
