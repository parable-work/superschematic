package rustgen

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// requiredNullUnionSchema has a shipment outcome that is either voided at an
// unknown time, with voidedAt present and null (D130), or delivered at a
// known one. Both members are @strictJSON. VoidedNote holds a RequiredNull
// field outside a union and is not @strictJSON.
func requiredNullUnionSchema() *ir.Schema {
	schema := ir.NewSchema("shipments", ir.SchemaKindGeneral)
	voided, delivered := "voided", "delivered"
	schema.Types["VoidedShipment"] = &ir.TypeDef{Name: "VoidedShipment", Role: ir.RoleEmbeddedStruct, StrictJSON: true, Fields: []*ir.FieldDef{
		{Name: "kind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: &voided},
		{Name: "voidedAt", Required: true, RequiredNull: true},
		{Name: "reason", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
	}}
	schema.Types["DeliveredShipment"] = &ir.TypeDef{Name: "DeliveredShipment", Role: ir.RoleEmbeddedStruct, StrictJSON: true, Fields: []*ir.FieldDef{
		{Name: "kind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: &delivered},
		{Name: "deliveredAt", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
	}}
	schema.Unions["ShipmentOutcome"] = &ir.UnionDef{Name: "ShipmentOutcome", Types: []string{"VoidedShipment", "DeliveredShipment"}}
	schema.Types["VoidedNote"] = &ir.TypeDef{Name: "VoidedNote", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
		{Name: "voidedAt", Required: true, RequiredNull: true},
	}}
	return schema
}

// TestRequiredNullSerde builds the Rust types of RequiredNull fields and runs
// serde on them (D130): the field is the unit type, so null decodes and
// encodes back as null, and an absent field or any other value is refused,
// in a union member and in a plain struct.
func TestRequiredNullSerde(t *testing.T) {
	output, err := Generate(requiredNullUnionSchema(), Options{SchemaName: "shipments", Clock: codegen.FixedClock(time.Unix(0, 0).UTC())})
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range output.Types {
		for _, field := range typ.Fields {
			if field.Name == "voidedAt" && field.RustType != "()" {
				t.Fatalf("%s.voidedAt is %s, want ()", typ.Name, field.RustType)
			}
		}
	}
	if testing.Short() {
		t.Skip("skipping compiled serde checks in short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping compiled serde checks")
	}
	crate := strings.ReplaceAll(output.CrateName, "-", "_")
	source := `use ` + crate + `::{ShipmentOutcome, VoidedNote};

#[test]
fn null_is_the_only_value() {
    let voided = r#"{"kind":"voided","voidedAt":null,"reason":"lost"}"#;
    let outcome: ShipmentOutcome = serde_json::from_str(voided).expect("decode the voided member");
    assert!(matches!(outcome, ShipmentOutcome::VoidedShipment(_)), "{outcome:?}");
    assert_eq!(serde_json::to_string(&outcome).unwrap(), voided);
    for payload in [
        r#"{"kind":"voided","reason":"lost"}"#,
        r#"{"kind":"voided","voidedAt":"2026-01-01","reason":"lost"}"#,
        r#"{"kind":"voided","voidedAt":0,"reason":"lost"}"#,
        r#"{"kind":"voided","voidedAt":false,"reason":"lost"}"#,
        r#"{"kind":"voided","voidedAt":{},"reason":"lost"}"#,
        r#"{"kind":"voided","voidedAt":[],"reason":"lost"}"#,
        r#"{"kind":"voided","voidedAt":null,"reason":"lost","deliveredAt":"2026-01-01"}"#,
    ] {
        assert!(serde_json::from_str::<ShipmentOutcome>(payload).is_err(), "accepted {payload}");
    }
    let note: VoidedNote = serde_json::from_str(r#"{"voidedAt":null}"#).expect("decode the note");
    assert_eq!(serde_json::to_string(&note).unwrap(), r#"{"voidedAt":null}"#);
    for payload in [r#"{}"#, r#"{"voidedAt":"2026-01-01"}"#, r#"{"voidedAt":1}"#] {
        assert!(serde_json::from_str::<VoidedNote>(payload).is_err(), "accepted {payload}");
    }
}
`
	cargoTestGeneratedCrate(t, cargoPath, output, filepath.Join(t.TempDir(), "target"), "required_null", source)
}
