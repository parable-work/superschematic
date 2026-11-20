package typegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// requiredNullSchema has a shipment that was voided at an unknown time: its
// voidedAt is present and null, and nothing else (D130). One type is
// @strictJSON and one is not. ShipmentOutcome is a union whose voided
// member holds such a field.
func requiredNullSchema() *ir.Schema {
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
	return schema
}

// TestGeneratedRequiredNullWireContract builds the Go types of a type with a
// RequiredNull field and runs them: a payload with the field null decodes and
// encodes back as it was, and one that leaves it out or gives it any other
// value is refused, in a @strictJSON type, in another and in a union member
// (D130).
func TestGeneratedRequiredNullWireContract(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping generated-module test run in -short mode")
	}
	paths := testpaths.Local(t)
	output, err := Generate(requiredNullSchema(), Options{
		SchemaName: "shipments",
		ModulePath: "example.com/schemas/types/go/shipments",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !output.HasRequiredNull {
		t.Fatal("HasRequiredNull is false")
	}
	dir := filepath.Join(t.TempDir(), "shipments")
	if err := SetReplacePaths(output, paths, dir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	const code = `package types

import (
	"encoding/json"
	"testing"
)

func TestRequiredNull(t *testing.T) {
	accepted := []byte(` + "`" + `{"voidedAt":null,"reason":"lost"}` + "`" + `)
	for name, decode := range map[string]func([]byte) (any, error){
		"plain":  func(b []byte) (any, error) { var v VoidedShipment; err := json.Unmarshal(b, &v); return &v, err },
		"strict": func(b []byte) (any, error) { var v StrictVoidedShipment; err := json.Unmarshal(b, &v); return &v, err },
	} {
		value, err := decode(accepted)
		if err != nil {
			t.Fatalf("%s: accept null: %v", name, err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != string(accepted) {
			t.Fatalf("%s: encoded %s, want %s", name, encoded, accepted)
		}
		for _, payload := range []string{
			` + "`" + `{"reason":"lost"}` + "`" + `,
			` + "`" + `{"voidedAt":"2026-01-01","reason":"lost"}` + "`" + `,
			` + "`" + `{"voidedAt":0,"reason":"lost"}` + "`" + `,
			` + "`" + `{"voidedAt":false,"reason":"lost"}` + "`" + `,
			` + "`" + `{"voidedAt":{},"reason":"lost"}` + "`" + `,
			` + "`" + `{"voidedAt":[],"reason":"lost"}` + "`" + `,
		} {
			if _, err := decode([]byte(payload)); err == nil {
				t.Fatalf("%s: accepted %s", name, payload)
			}
		}
	}
	var outcome ShipmentOutcomeWrapper
	if err := json.Unmarshal([]byte(` + "`" + `{"kind":"voided","voidedAt":null}` + "`" + `), &outcome); err != nil {
		t.Fatalf("union: accept null: %v", err)
	}
	if _, ok := outcome.Value.(VoidedOutcome); !ok {
		t.Fatalf("union: picked %T", outcome.Value)
	}
	for _, payload := range []string{
		` + "`" + `{"kind":"voided"}` + "`" + `,
		` + "`" + `{"kind":"voided","voidedAt":"2026-01-01"}` + "`" + `,
		` + "`" + `{"kind":"voided","voidedAt":0}` + "`" + `,
	} {
		if err := json.Unmarshal([]byte(payload), &ShipmentOutcomeWrapper{}); err == nil {
			t.Fatalf("union: accepted %s", payload)
		}
	}
}
`
	if err := os.WriteFile(filepath.Join(dir, "required_null_test.go"), []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
	}
	run := exec.Command("go", "test", "-count=1", "-run", "^TestRequiredNull$", ".")
	run.Dir = dir
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("generated RequiredNull test: %v\n%s", err, out)
	}
}
