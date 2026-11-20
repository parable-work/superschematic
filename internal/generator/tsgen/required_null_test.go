package tsgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestRequiredNullTypeAndValidator generates the TypeScript types package
// of a type with a RequiredNull field, type-checks it, and runs its
// validator (D130): the field's type is null, null passes, and any other
// value, or none, fails as "expected null", in a @strictJSON type and in
// another. A union reads its member with the field null and in no other way.
func TestRequiredNullTypeAndValidator(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		requireOrSkipTSTooling(t, "bun unavailable")
	}
	paths := testpaths.Local(t)
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
	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tempRoot, "types", "typescript", "shipments")
	if err := SetScalarLibSpec(output, paths, dir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	types, err := os.ReadFile(filepath.Join(dir, "types", "types.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(types), "voidedAt: null;") {
		t.Fatalf("types.ts does not declare voidedAt: null:\n%s", types)
	}
	if err := (WorkspaceRoot{OutputRoot: tempRoot, Paths: paths}).Write(); err != nil {
		t.Fatal(err)
	}
	install := exec.Command(bunPath, "install")
	install.Dir = dir
	if out, err := install.CombinedOutput(); err != nil {
		requireOrSkipTSTooling(t, "bun install failed (likely offline): "+err.Error()+"\n"+string(out))
	}
	check := exec.Command(bunPath, "x", "tsc", "--noEmit")
	check.Dir = dir
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("generated RequiredNull package does not type-check: %v\n%s", err, out)
	}
	const probe = `import { expect, test } from 'bun:test';
import { parseShipmentOutcomeFromJSON, validateStrictVoidedShipment, validateVoidedShipment } from './validators';

for (const [name, validate] of [['plain', validateVoidedShipment], ['strict', validateStrictVoidedShipment]] as const) {
  test(name + ': null passes and every other value fails', () => {
    expect(validate({ voidedAt: null, reason: 'lost' } as never)).toBe(true);
    for (const voidedAt of ['2026-01-01', 0, false, {}, [], undefined]) {
      const result = validate({ voidedAt, reason: 'lost' } as never);
      expect(result).not.toBe(true);
      expect(JSON.stringify(result)).toContain('expected null');
    }
  });
}

test('union: the member reads null and nothing else', () => {
  expect(parseShipmentOutcomeFromJSON({ kind: 'voided', voidedAt: null })).toEqual({ kind: 'voided', voidedAt: null } as never);
  for (const payload of [{ kind: 'voided' }, { kind: 'voided', voidedAt: '2026-01-01' }, { kind: 'voided', voidedAt: 0 }]) {
    expect(() => parseShipmentOutcomeFromJSON(payload)).toThrow();
  }
});
`
	if err := os.WriteFile(filepath.Join(dir, "required_null.test.ts"), []byte(probe), 0o644); err != nil {
		t.Fatal(err)
	}
	run := exec.Command(bunPath, "test", "./required_null.test.ts")
	run.Dir = dir
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("RequiredNull validator test: %v\n%s", err, out)
	}
}
