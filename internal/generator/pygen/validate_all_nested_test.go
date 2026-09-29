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

// nestedRulesSchema declares an input that holds models with rules of their
// own at every depth validate_all walks: a list (lines), a field (shipTo),
// a list of lists (grid), a map (extras), a map of lists (extraLists) and a
// type that holds itself (category.children). productId, postalCode and
// extraLists have wire names that differ from their snake_case names.
func nestedRulesSchema() *ir.Schema {
	listMin, minLength, maxLength := 1, 3, 4
	minQuantity, maxQuantity := 1.0, 99.0
	schema := ir.NewSchema("nested-rules", ir.SchemaKindGeneral)
	schema.Types["OrderLine"] = &ir.TypeDef{
		Name: "OrderLine",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "productId", TypeRef: ir.TypeRef{Name: "string"}, Required: true, ValidateMinLength: &minLength},
			{Name: "quantity", TypeRef: ir.TypeRef{Name: "number"}, Required: true, ValidateMin: &minQuantity, ValidateMax: &maxQuantity},
		},
	}
	schema.Types["ShippingAddress"] = &ir.TypeDef{
		Name: "ShippingAddress",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "postalCode", TypeRef: ir.TypeRef{Name: "string"}, Required: true, ValidatePattern: "^[0-9]{5}$"},
		},
	}
	schema.Types["Category"] = &ir.TypeDef{
		Name: "Category",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true, ValidateMaxLength: &maxLength},
			{Name: "children", TypeRef: ir.TypeRef{Name: "Category", IsArray: true}},
		},
	}
	schema.Types["PlaceOrderInput"] = &ir.TypeDef{
		Name: "PlaceOrderInput",
		Role: ir.RoleAPIInput,
		Fields: []*ir.FieldDef{
			{Name: "lines", TypeRef: ir.TypeRef{Name: "OrderLine", IsArray: true}, Required: true, ValidateListMin: &listMin},
			{Name: "shipTo", TypeRef: ir.TypeRef{Name: "ShippingAddress"}},
			{Name: "grid", TypeRef: ir.TypeRef{Name: "OrderLine", IsArray: true, IsArrayOfArrays: true}},
			{Name: "extras", TypeRef: ir.TypeRef{Name: "OrderLine", IsMap: true}},
			{Name: "extraLists", TypeRef: ir.TypeRef{Name: "OrderLine", IsMap: true, IsArray: true}},
			{Name: "category", TypeRef: ir.TypeRef{Name: "Category"}},
			{Name: "note", TypeRef: ir.TypeRef{Name: "string"}, ValidateMaxLength: &maxLength},
		},
	}
	return schema
}

func writeNestedRulesTypes(t *testing.T) (string, *ModuleOutput) {
	t.Helper()
	output, err := Generate(nestedRulesSchema(), Options{
		SchemaName: "nested-rules",
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

// TestValidateAllNestedRenders pins where validate_all walks nested models:
// once per field whose values are generated models, at any depth, and not
// for a field of a builtin type. A module with no such field renders no
// walker.
func TestValidateAllNestedRenders(t *testing.T) {
	outDir, output := writeNestedRulesTypes(t)
	typesPy, err := os.ReadFile(filepath.Join(outDir, output.PythonModuleName, "types.py"))
	if err != nil {
		t.Fatalf("read types.py: %v", err)
	}
	src := string(typesPy)
	if got := strings.Count(src, "def _add_model_errors("); got != 1 {
		t.Errorf("types.py defines _add_model_errors %d times, want 1", got)
	}
	for _, field := range []struct{ key, attr string }{
		{"lines", "lines"},
		{"ship_to", "ship_to"},
		{"grid", "grid"},
		{"extras", "extras"},
		{"extra_lists", "extra_lists"},
		{"category", "category"},
		{"children", "children"},
	} {
		call := `_add_model_errors(errors, "` + field.key + `", self.` + field.attr + `, by_alias)`
		if got := strings.Count(src, call); got != 1 {
			t.Errorf("types.py renders %q %d times, want 1", call, got)
		}
	}
	if strings.Contains(src, `_add_model_errors(errors, "note"`) {
		t.Errorf("types.py walks note, a string field")
	}

	wireDir, wireOutput := writeWireNamesTypes(t)
	wirePy, err := os.ReadFile(filepath.Join(wireDir, wireOutput.PythonModuleName, "types.py"))
	if err != nil {
		t.Fatalf("read wire-names types.py: %v", err)
	}
	if strings.Contains(string(wirePy), "_add_model_errors") {
		t.Errorf("wire-names types.py renders _add_model_errors; none of its fields holds a model")
	}
}

// TestValidateAllNestedRun imports the generated nested-rules package and
// checks at run time that validate_all reports the rules of every model a
// field holds, once each, under the path that reaches it: snake_case names
// by default and wire names with by_alias, map keys as given. A None list
// item is the list's "required" alone, and a dict model_construct leaves
// in place of a model (the parity driver's shape) is not walked. Skips when
// python3 or pydantic is unavailable.
func TestValidateAllNestedRun(t *testing.T) {
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
	outDir, output := writeNestedRulesTypes(t)

	command := exec.Command(python, "-B", "-c", `
import importlib
import sys

mod = importlib.import_module(sys.argv[1])
PlaceOrderInput, OrderLine = mod.PlaceOrderInput, mod.OrderLine


def verdicts(errors):
    return {key: [e["validator"] for e in entries] for key, entries in errors.errors.items()}


order = PlaceOrderInput.model_validate({
    "lines": [{"productId": "abc", "quantity": 0}, {"productId": "x", "quantity": 2}],
    "shipTo": {"postalCode": "ABCDE"},
    "grid": [[{"productId": "abc", "quantity": 1}, {"productId": "abc", "quantity": 100}]],
    "extras": {"gift": {"productId": "abc", "quantity": 0}},
    "extraLists": {"gift": [{"productId": "no", "quantity": 1}]},
    "category": {"name": "ok", "children": [{"name": "too long", "children": []}]},
    "note": "too long",
}, strict=True)
assert verdicts(order.validate_all()) == {
    "lines[0].quantity": ["min"],
    "lines[1].product_id": ["minLength"],
    "ship_to.postal_code": ["pattern"],
    "grid[0][1].quantity": ["max"],
    "extras.gift.quantity": ["min"],
    "extra_lists.gift[0].product_id": ["minLength"],
    "category.children[0].name": ["maxLength"],
    "note": ["maxLength"],
}, verdicts(order.validate_all())
assert verdicts(order.validate_all(by_alias=True)) == {
    "lines[0].quantity": ["min"],
    "lines[1].productId": ["minLength"],
    "shipTo.postalCode": ["pattern"],
    "grid[0][1].quantity": ["max"],
    "extras.gift.quantity": ["min"],
    "extraLists.gift[0].productId": ["minLength"],
    "category.children[0].name": ["maxLength"],
    "note": ["maxLength"],
}, verdicts(order.validate_all(by_alias=True))

# A model reports its own fields as before; nested models only add to them.
assert verdicts(PlaceOrderInput.model_validate({"lines": []}).validate_all()) == {"lines": ["listMin"]}
assert verdicts(OrderLine.model_validate({"productId": "x", "quantity": 0}).validate_all(by_alias=True)) == {
    "productId": ["minLength"],
    "quantity": ["min"],
}

# A None item is the list's to report, once; the models beside it are walked.
bad_line = OrderLine.model_validate({"productId": "abc", "quantity": 0})
with_none = PlaceOrderInput.model_construct(lines=[None, bad_line])
assert verdicts(with_none.validate_all(by_alias=True)) == {
    "lines[0]": ["required"],
    "lines[1].quantity": ["min"],
}, verdicts(with_none.validate_all(by_alias=True))

# The parity driver's model_construct leaves nested dicts unparsed; they are
# not walked, so its verdicts are the input's own fields'.
unparsed = PlaceOrderInput.model_construct(
    lines=[{"productId": "x", "quantity": 0}],
    ship_to={"postalCode": "ABCDE"},
    extras={"gift": {"productId": "x", "quantity": 0}},
)
assert verdicts(unparsed.validate_all()) == {}, verdicts(unparsed.validate_all())
`, output.PythonModuleName)
	command.Env = append(os.Environ(), "PYTHONPATH="+outDir)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated nested-rules package run failed: %v\n%s", err, out)
	}
}
