package writer

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestTSWriterWritesDeriverAndRunnerSettings: a settings element that
// names a deriver beside its API's handle (D74), or the engine's runner
// beside its database's (D58), is written with its deriver or runner key,
// as the reader reads it back.
func TestTSWriterWritesDeriverAndRunnerSettings(t *testing.T) {
	api := ir.ServiceRef{Name: "shop-cart", Kind: ir.SchemaKindAPI}
	db := ir.ServiceRef{Name: "shop-db", Kind: ir.SchemaKindDB}
	off, one := false, 1
	schema := ir.NewSchema("shop-stack", ir.SchemaKindStack)
	schema.Types["Shop"] = &ir.TypeDef{Name: "Shop", Role: ir.RoleEmbeddedStruct, Stack: &ir.StackDecl{Deploy: []ir.ServiceRef{api}}}
	schema.Types["Dev"] = &ir.TypeDef{Name: "Dev", Role: ir.RoleEmbeddedStruct, Environment: &ir.EnvironmentDecl{
		Target: "local",
		Settings: []*ir.DeployableSettings{
			{Of: ir.DeployableRef{Service: &api, Deriver: "TriageTickets"}, Instances: &one, Enabled: &off},
			{Of: ir.DeployableRef{Service: &db, Runner: true}, Enabled: &off},
		},
	}}
	source := writtenSource(t, schema, FormatTS)
	for _, want := range []string{
		`{ of: ShopCart, deriver: "TriageTickets", enabled: false, instances: 1 }`,
		`{ of: ShopDb, runner: true, enabled: false }`,
	} {
		if !strings.Contains(source, want) {
			t.Errorf("written TypeScript lacks %s:\n%s", want, source)
		}
	}
}
