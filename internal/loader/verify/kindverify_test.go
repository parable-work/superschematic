package verify

import (
	"strings"
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

// TestRunCallsRegisteredChecksOnCoreKinds: a CheckSpec reaches schemas of a
// core kind, which an extension cannot attach a KindSpec.Verify to. It runs
// after the kind's own Verify, only on the kinds it lists.
func TestRunCallsRegisteredChecksOnCoreKinds(t *testing.T) {
	var order []string
	reg := registry.New(naming.Naming{})
	for _, spec := range []registry.CheckSpec{
		{
			Name: "everyKind", Extension: "policy",
			Verify: func(schema *ir.Schema, r registry.VerifyReporter) {
				order = append(order, "everyKind:"+schema.Name)
				r.Errorf("", "%s breaks the policy", schema.Name)
			},
		},
		{
			Name: "apiOnly", Extension: "policy", Kinds: []string{string(ir.SchemaKindAPI)},
			Verify: func(schema *ir.Schema, r registry.VerifyReporter) {
				order = append(order, "apiOnly:"+schema.Name)
				r.Warnf("src/api.schema.ts", "%s is an API", schema.Name)
			},
		},
	} {
		if err := reg.RegisterCheck(spec); err != nil {
			t.Fatal(err)
		}
	}

	api := Run(ir.NewSchema("shop-api", ir.SchemaKindAPI), Input{Registry: reg})
	if !hasError(api, "shop-api breaks the policy") || !hasWarning(api, "shop-api is an API") {
		t.Errorf("API findings = %v, warnings %v", errorStrings(api), api.Warnings)
	}
	db := Run(ir.NewSchema("shop-db", ir.SchemaKindDB), Input{Registry: reg})
	if !hasError(db, "shop-db breaks the policy") || len(db.Warnings) != 0 {
		t.Errorf("DB findings = %v, warnings %v", errorStrings(db), db.Warnings)
	}
	if got := strings.Join(order, ","); got != "everyKind:shop-api,apiOnly:shop-api,everyKind:shop-db" {
		t.Errorf("checks ran as %s", got)
	}
}
