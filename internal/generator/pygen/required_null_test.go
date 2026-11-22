package pygen

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// TestRequiredNullModels generates the Python models of a type with a
// RequiredNull field and runs them (D130): a payload with the field null
// parses, validates and dumps back as it was, and one that leaves it out or
// gives it any other value is refused, in a @strictJSON type, in another and
// in a union member.
func TestRequiredNullModels(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping Python probe in -short mode")
	}
	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	if err := exec.Command(pythonPath, "-c", "import pydantic").Run(); err != nil {
		t.Skip("pydantic not available")
	}
	schema := ir.NewSchema("shipments", ir.SchemaKindGeneral)
	for _, strict := range []bool{false, true} {
		name := "VoidedShipment"
		if strict {
			name = "StrictVoidedShipment"
		}
		schema.Types[name] = &ir.TypeDef{Name: name, Role: ir.RoleEmbeddedStruct, StrictJSON: strict, Fields: []*ir.FieldDef{
			{Name: "voidedAt", Required: true, RequiredNull: true},
			{Name: "reason", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		}}
	}
	voided, delivered := "voided", "delivered"
	schema.Types["VoidedOutcome"] = &ir.TypeDef{Name: "VoidedOutcome", Role: ir.RoleEmbeddedStruct, StrictJSON: true, Fields: []*ir.FieldDef{
		{Name: "kind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: &voided},
		{Name: "voidedAt", Required: true, RequiredNull: true},
	}}
	schema.Types["DeliveredOutcome"] = &ir.TypeDef{Name: "DeliveredOutcome", Role: ir.RoleEmbeddedStruct, StrictJSON: true, Fields: []*ir.FieldDef{
		{Name: "kind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: &delivered},
		{Name: "deliveredAt", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
	}}
	schema.Unions["ShipmentOutcome"] = &ir.UnionDef{Name: "ShipmentOutcome", Types: []string{"VoidedOutcome", "DeliveredOutcome"}}
	output, err := Generate(schema, Options{SchemaName: "shipments", Clock: codegen.FixedClock(time.Unix(0, 0).UTC())})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "shipments")
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`import json
import sys
sys.path.insert(0, %q)
from pydantic import ValidationError
from %s.types import StrictVoidedShipment, VoidedOutcome, VoidedShipment
from %s.unions import pick_union_member

accepted = {"voidedAt": None, "reason": "lost"}
for model in (VoidedShipment, StrictVoidedShipment):
    value = model.from_dict(accepted)
    assert value.voided_at is None
    assert not value.validate_all().has_errors(), value.validate_all().errors
    assert value.to_dict() == accepted, value.to_dict()
    assert value.to_json_dict() == accepted, value.to_json_dict()
    assert json.loads(value.to_json()) == accepted, value.to_json()
    assert model.from_json(json.dumps(accepted)) == value
    for payload in ({"reason": "lost"}, {"voidedAt": "2026-01-01", "reason": "lost"}, {"voidedAt": 0, "reason": "lost"}, {"voidedAt": False, "reason": "lost"}, {"voidedAt": {}, "reason": "lost"}, {"voidedAt": [], "reason": "lost"}):
        try:
            model.from_dict(payload)
        except ValidationError:
            pass
        else:
            raise AssertionError(f"{model.__name__} accepted {payload}")
    changed = model.model_construct(voided_at="2026-01-01", reason="lost")
    errors = changed.validate_all().errors
    assert [e["validator"] for e in errors["voided_at"]] == ["invalid"], errors

assert pick_union_member("ShipmentOutcome", {"kind": "voided", "voidedAt": None}) == 0
for payload in ({"kind": "voided"}, {"kind": "voided", "voidedAt": "2026-01-01"}, {"kind": "voided", "voidedAt": 0}):
    assert pick_union_member("ShipmentOutcome", payload) < 0, payload
assert VoidedOutcome.from_dict({"kind": "voided", "voidedAt": None}).voided_at is None
`, dir, output.PythonModuleName, output.PythonModuleName)
	probe := exec.Command(pythonPath, "-W", "error", "-c", script)
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("RequiredNull Python probe: %v\n%s", err, out)
	}
}
