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

// nestedArrayRulesSchema declares lists of lists that carry every
// per-element rule validate_all renders, outer list bounds, an object
// element type with a secret field, and a secret list of lists. The
// fixture-nested-arrays services declare no element rules, so this schema
// covers them.
func nestedArrayRulesSchema() *ir.Schema {
	minLength, maxLength, listMin, listMax := 2, 4, 1, 3
	minValue, maxValue := 0.0, 10.0
	nested := func(name string) ir.TypeRef {
		return ir.TypeRef{Name: name, IsArray: true, IsArrayOfArrays: true}
	}

	schema := ir.NewSchema("nested-rules", ir.SchemaKindGeneral)
	schema.Types["Cell"] = &ir.TypeDef{
		Name: "Cell",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "label", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			{Name: "token", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Secret: true},
		},
	}
	schema.Types["Sheet"] = &ir.TypeDef{
		Name: "Sheet",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{
				Name:              "codes",
				TypeRef:           nested("string"),
				Required:          true,
				ValidateMinLength: &minLength,
				ValidateMaxLength: &maxLength,
				ValidatePattern:   "^[a-z]+$",
				ValidateListMin:   &listMin,
				ValidateListMax:   &listMax,
			},
			{Name: "scores", TypeRef: nested("number"), ValidateMin: &minValue, ValidateMax: &maxValue},
			{Name: "cells", TypeRef: nested("Cell"), Required: true},
			{Name: "optionalCells", TypeRef: nested("Cell")},
			{Name: "secretRows", TypeRef: nested("string"), Required: true, Secret: true},
		},
	}
	return schema
}

// TestNestedArrayRulesRender pins the validate_all and mask_secrets code a
// list of lists renders: element rules visit every innermost element and
// report field[i][j], list bounds read the outer list, a None inner list is
// reported at field[i], and masking maps each inner list.
func TestNestedArrayRulesRender(t *testing.T) {
	output, err := Generate(nestedArrayRulesSchema(), Options{
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
	typesPy, err := os.ReadFile(filepath.Join(outDir, output.PythonModuleName, "types.py"))
	if err != nil {
		t.Fatalf("read types.py: %v", err)
	}

	for _, want := range []string{
		`codes: List[List[str]] = Field(..., alias="codes", serialization_alias="codes")`,
		`scores: Optional[List[List[float]]] = Field(default=None, alias="scores", serialization_alias="scores")`,
		`cells: List[List[Cell]] = Field(..., alias="cells", serialization_alias="cells")`,
		`                    if row is None:
                        errors.add_field_error(f"codes[{index}]", "required", "required field")
                    elif not isinstance(row, list):
                        errors.add_field_error(f"codes[{index}]", "type", "expected an array")
                    else:
                        for inner_index, item in enumerate(row):
                            if item is None:
                                errors.add_field_error(f"codes[{index}][{inner_index}]", "required", "required field")`,
		// An optional list of lists skips the whole-value check when a None
		// entry is reported at its own index.
		`            if not (isinstance(self.scores, list) and any(row is None or not isinstance(row, list) or None in row for row in self.scores)):
                try:
                    TypeAdapter(List[List[float]]).validate_python(self.scores)`,
		`            if isinstance(self.codes, list) and len(self.codes) < 1:
                errors.add_field_error("codes", "listMin", "must contain at least 1 items")`,
		`            if isinstance(self.codes, list) and len(self.codes) > 3:
                errors.add_field_error("codes", "listMax", "must contain at most 3 items")`,
		`                for index, row in enumerate(self.codes):
                    if not isinstance(row, list):
                        continue
                    for inner_index, item in enumerate(row):
                        if item is not None and len(str(item)) < 2:
                            errors.add_field_error(f"codes[{index}][{inner_index}]", "minLength", "must be at least 2 characters")`,
		`                        if item is not None and len(str(item)) > 4:
                            errors.add_field_error(f"codes[{index}][{inner_index}]", "maxLength", "must be at most 4 characters")`,
		`                        if item is not None and re.search(r"^[a-z]+$", str(item)) is None:
                            errors.add_field_error(f"codes[{index}][{inner_index}]", "pattern", "invalid format")`,
		`                        if item is not None and float(item) < 0:
                            errors.add_field_error(f"scores[{index}][{inner_index}]", "min", "must be at least 0")`,
		`                        if item is not None and float(item) > 10:
                            errors.add_field_error(f"scores[{index}][{inner_index}]", "max", "must be at most 10")`,
		`"cells": [[item.mask_secrets() for item in row] for row in self.cells],`,
		`"optional_cells": [[item.mask_secrets() for item in row] for row in self.optional_cells] if self.optional_cells is not None else None,`,
		`"secret_rows": [],`,
	} {
		if !strings.Contains(string(typesPy), want) {
			t.Errorf("types.py missing:\n%s\n--- types.py ---\n%s", want, typesPy)
		}
	}
	for _, unwanted := range []string{`"codes": `, `"scores": `} {
		if strings.Contains(string(typesPy), unwanted) {
			t.Errorf("mask_secrets must leave scalar lists of lists unchanged; found %s", unwanted)
		}
	}
}

// TestGeneratedNestedArraysRun imports the generated nested-rules package and
// checks validate_all and mask_secrets at run time. Skips when python3 or
// pydantic is unavailable; runtime/schema/python runs the fixture package
// under pytest.
func TestGeneratedNestedArraysRun(t *testing.T) {
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

	output, err := Generate(nestedArrayRulesSchema(), Options{
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

	command := exec.Command(python, "-B", "-c", `
import importlib
import sys

mod = importlib.import_module(sys.argv[1])
Sheet, Cell = mod.Sheet, mod.Cell

def cell(label):
    return {"label": label, "token": "t-" + label}

def verdicts(sheet):
    return {key: sorted(e["validator"] for e in entries) for key, entries in sheet.validate_all().errors.items()}

valid = Sheet.model_validate({
    "codes": [["ab", "abcd"], [], ["xyz"]],
    "scores": [[0, 10], [], [2.5, 3, 4]],
    "cells": [[cell("a")], [], [cell("b"), cell("c")]],
    "optionalCells": [[], [cell("d")]],
    "secretRows": [["s"]],
}, strict=True)
assert verdicts(valid) == {}, verdicts(valid)

# Element rules report both indexes; list bounds read the outer list only.
bad = Sheet.model_construct(
    codes=[["ab", "a"], ["TOOLONG", "AB"], [], ["ok"]],
    scores=[[1], [11, -1]],
    cells=[[]],
    secret_rows=[],
)
assert verdicts(bad) == {
    "codes": ["listMax"],
    "codes[0][1]": ["minLength"],
    "codes[1][0]": ["maxLength", "pattern"],
    "codes[1][1]": ["pattern"],
    "scores[1][0]": ["max"],
    "scores[1][1]": ["min"],
}, verdicts(bad)

# Inner lists and elements are never None: validate_all names the index,
# once, without a whole-value "invalid" on the optional field.
nulls = Sheet.model_construct(codes=[["ab"], None], scores=[None, [1, None]], cells=[None], secret_rows=[])
assert verdicts(nulls) == {
    "codes[1]": ["required"],
    "cells[0]": ["required"],
    "scores[0]": ["required"],
    "scores[1][1]": ["required"],
}, verdicts(nulls)

# A non-list inner value is a type error at its index.
rows = Sheet.model_construct(codes=[["ab"], "cd"], cells=[], secret_rows=[], scores=[[1], 2])
assert verdicts(rows) == {"codes[1]": ["type"], "scores[1]": ["type"]}, verdicts(rows)

# Masking maps every inner list and zeroes a secret list of lists.
masked = valid.mask_secrets()
assert [[c.label for c in row] for row in masked.cells] == [["a"], [], ["b", "c"]]
assert all(c.token == "" for row in masked.cells for c in row)
assert all(c.token == "" for row in masked.optional_cells for c in row)
assert masked.secret_rows == []
assert masked.codes == valid.codes
assert Sheet.model_construct(codes=[["ab"]], cells=[], secret_rows=[], optional_cells=None).mask_secrets().optional_cells is None
`, output.PythonModuleName)
	command.Env = append(os.Environ(), "PYTHONPATH="+outDir)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated nested-array package run failed: %v\n%s", err, out)
	}
}
