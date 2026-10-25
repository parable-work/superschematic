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

// TestTSWriterWritesApps: an app is exposed, and set in an environment's
// settings, beside its API's handle (D78), as the reader reads it back.
func TestTSWriterWritesApps(t *testing.T) {
	api := ir.ServiceRef{Name: "shop-stock", Kind: ir.SchemaKindAPI}
	off := false
	schema := ir.NewSchema("shop-stack", ir.SchemaKindStack)
	schema.Types["Shop"] = &ir.TypeDef{Name: "Shop", Role: ir.RoleEmbeddedStruct, Stack: &ir.StackDecl{
		Deploy: []ir.ServiceRef{api},
		Expose: []ir.DeployableRef{{Service: &api}, {Service: &api, App: "Backoffice"}},
	}}
	schema.Types["Dev"] = &ir.TypeDef{Name: "Dev", Role: ir.RoleEmbeddedStruct, Environment: &ir.EnvironmentDecl{
		Target:   "local",
		Settings: []*ir.DeployableSettings{{Of: ir.DeployableRef{Service: &api, App: "Backoffice"}, Enabled: &off, Values: map[string]any{"port": float64(3000)}}},
	}}
	source := writtenSource(t, schema, FormatTS)
	for _, want := range []string{
		`expose: [ShopStock, { of: ShopStock, app: "Backoffice" }]`,
		`{ of: ShopStock, app: "Backoffice", enabled: false, port: 3000 }`,
	} {
		if !strings.Contains(source, want) {
			t.Errorf("written TypeScript lacks %s:\n%s", want, source)
		}
	}
}
