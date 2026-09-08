package verify

import (
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// TestRunCallsTheKindSpecVerifyHook: a registered kind's Verify runs on a
// schema of that kind and its findings land in the Result with the severity
// it chose; a schema of another kind never sees the hook.
func TestRunCallsTheKindSpecVerifyHook(t *testing.T) {
	var seen []string
	reg := registry.New(naming.Naming{})
	err := reg.RegisterKind(registry.KindSpec{
		Name: "Grouping", Extension: "test", StructRole: ir.RoleEmbeddedStruct,
		Verify: func(schema *ir.Schema, r registry.VerifyReporter) {
			seen = append(seen, schema.Name)
			r.Errorf("src/g.schema.ts", "grouping %s has no members", schema.Name)
			r.Warnf("", "grouping %s is empty", schema.Name)
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	r := Run(ir.NewSchema("grp", ir.SchemaKind("Grouping")), Input{Registry: reg})
	if !hasError(r, "src/g.schema.ts: grouping grp has no members") {
		t.Errorf("Verify error not reported, got %v", errorStrings(r))
	}
	if !hasWarning(r, "grouping grp is empty") {
		t.Errorf("Verify warning not reported, got %v", r.Warnings)
	}
	if len(seen) != 1 || seen[0] != "grp" {
		t.Errorf("Verify ran for %v, want [grp]", seen)
	}

	other := Run(ir.NewSchema("db", ir.SchemaKindDB), Input{Registry: reg})
	if len(other.Errors) != 0 || len(seen) != 1 {
		t.Errorf("Verify of another kind ran the Grouping hook: errors %v, seen %v", errorStrings(other), seen)
	}
}
