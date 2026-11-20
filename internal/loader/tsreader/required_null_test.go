package tsreader

import (
	"reflect"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestRequiredNullLoads reads RequiredNull on a class field and on a type
// alias field (D130): each is a required field with no value type.
func TestRequiredNullLoads(t *testing.T) {
	dir := decoratorTestService(t, "General", map[string]string{"src/a.schema.ts": `import { RequiredNull, strictJSON } from "@superschematic/schema";
@strictJSON
export abstract class VoidedShipment {
  // The time is not known.
  voidedAt: RequiredNull;
  reason: string;
}
export type VoidedNote = {
  voidedAt: RequiredNull;
};
`})
	schema, _, err := LoadService(dir)
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	for _, name := range []string{"VoidedShipment", "VoidedNote"} {
		field := schema.Types[name].Fields[0]
		want := ir.FieldDef{Name: "voidedAt", Required: true, RequiredNull: true, Comment: field.Comment}
		if !reflect.DeepEqual(*field, want) {
			t.Errorf("%s.voidedAt = %+v, want %+v", name, *field, want)
		}
	}
	if comment := schema.Types["VoidedShipment"].Fields[0].Comment; !strings.Contains(comment, "not known") {
		t.Errorf("VoidedShipment.voidedAt comment = %q", comment)
	}
}

// TestRequiredNullRefusedForms pins the diagnostics for the places
// RequiredNull has no meaning (D130): with a type argument, which the
// TypeScript checker refuses first, as a list element or Record value,
// inside a wrapper and on an optional field.
func TestRequiredNullRefusedForms(t *testing.T) {
	cases := []struct {
		name   string
		source string
		want   string
	}{
		{"type argument", "export abstract class Shipment {\n  voidedAt: RequiredNull<string>;\n}\n", "Type 'RequiredNull' is not generic"},
		{"list element", "export abstract class Shipment {\n  voidedAt: RequiredNull[];\n}\n", "RequiredNull cannot be a list element"},
		{"record value", "export abstract class Shipment {\n  voidedAt: Record<string, RequiredNull>;\n}\n", "RequiredNull cannot be a Record value"},
		{"wrapper", "export abstract class Shipment {\n  voidedAt: Nullable<RequiredNull>;\n}\n", "RequiredNull cannot be combined with Nullable"},
		{"optional", "export abstract class Shipment {\n  voidedAt?: RequiredNull;\n}\n", "invalid required-null field definition"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := decoratorTestService(t, "General", map[string]string{"src/a.schema.ts": `import { Nullable, RequiredNull } from "@superschematic/schema";
` + tc.source})
			_, _, err := LoadService(dir)
			if err == nil {
				t.Fatal("expected a schema error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
