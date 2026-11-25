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

// TestTSWriterWritesAWorkersMode: a worker's settings element is written
// with its mode, its idle time and its maxInstances, as the reader reads
// them back (D145, D53).
func TestTSWriterWritesAWorkersMode(t *testing.T) {
	api := ir.ServiceRef{Name: "shop-orders", Kind: ir.SchemaKindAPI}
	two, four := 2, 4
	schema := ir.NewSchema("shop-stack", ir.SchemaKindStack)
	schema.Types["Shop"] = &ir.TypeDef{Name: "Shop", Role: ir.RoleEmbeddedStruct, Stack: &ir.StackDecl{Deploy: []ir.ServiceRef{api}}}
	schema.Types["Staging"] = &ir.TypeDef{Name: "Staging", Role: ir.RoleEmbeddedStruct, Environment: &ir.EnvironmentDecl{
		Target: "gcp",
		Settings: []*ir.DeployableSettings{
			{Of: ir.DeployableRef{Service: &api, Worker: "FulfilOrders"}, Mode: ir.WorkerModeDrain, Idle: "1m", Instances: &two, Values: map[string]any{"tick": "10m"}},
			{Of: ir.DeployableRef{Service: &api, Worker: "SendReceipts"}, Instances: &two, MaxInstances: &four},
		},
	}}
	source := writtenSource(t, schema, FormatTS)
	for _, want := range []string{
		`worker: "FulfilOrders"`, `mode: "drain"`, `idle: "1m"`, `tick: "10m"`,
		`worker: "SendReceipts"`, `maxInstances: 4`,
	} {
		if !strings.Contains(source, want) {
			t.Errorf("written TypeScript lacks %s:\n%s", want, source)
		}
	}
}

// TestTSWriterWritesOrigins: @stack's origins are written with each site's
// handle and the APIs its origin serves, as the reader reads them back
// (D91).
func TestTSWriterWritesOrigins(t *testing.T) {
	api := ir.ServiceRef{Name: "shop-api", Kind: ir.SchemaKindAPI}
	web := ir.ServiceRef{Name: "shop-web", Kind: ir.SchemaKindSite}
	admin := ir.ServiceRef{Name: "shop-admin", Kind: ir.SchemaKindSite}
	schema := ir.NewSchema("shop-stack", ir.SchemaKindStack)
	schema.Types["Shop"] = &ir.TypeDef{Name: "Shop", Role: ir.RoleEmbeddedStruct, Stack: &ir.StackDecl{
		Deploy:  []ir.ServiceRef{web, admin},
		Origins: []*ir.Origin{{Site: web, APIs: []ir.ServiceRef{api}}, {Site: admin}},
	}}
	schema.Types["Dev"] = &ir.TypeDef{Name: "Dev", Role: ir.RoleEmbeddedStruct, Environment: &ir.EnvironmentDecl{Target: "local"}}
	source := writtenSource(t, schema, FormatTS)
	if want := `origins: [{ site: ShopWeb, apis: [ShopApi] }, { site: ShopAdmin }]`; !strings.Contains(source, want) {
		t.Errorf("written TypeScript lacks %s:\n%s", want, source)
	}
}

// TestTSWriterWritesImages: @stack's images are written with each
// deployable as a settings element names one, its packages and the
// Dockerfile that extends it, as the reader reads them back (D144).
func TestTSWriterWritesImages(t *testing.T) {
	api := ir.ServiceRef{Name: "shop-api", Kind: ir.SchemaKindAPI}
	orders := ir.ServiceRef{Name: "shop-orders", Kind: ir.SchemaKindAPI}
	db := ir.ServiceRef{Name: "shop-db", Kind: ir.SchemaKindDB}
	schema := ir.NewSchema("shop-stack", ir.SchemaKindStack)
	schema.Types["Backend"] = &ir.TypeDef{Name: "Backend", Role: ir.RoleEmbeddedStruct, Server: &ir.ServerDecl{Serves: []ir.ServiceRef{orders}}}
	schema.Types["Shop"] = &ir.TypeDef{Name: "Shop", Role: ir.RoleEmbeddedStruct, Stack: &ir.StackDecl{
		Deploy: []ir.ServiceRef{api, orders},
		Images: []*ir.DeployableImage{
			{Of: ir.DeployableRef{Service: &api}, Packages: []string{"fonts-liberation", "chromium"}},
			{Of: ir.DeployableRef{Deployable: "Backend"}, Extend: "docker/backend.Dockerfile"},
			{Of: ir.DeployableRef{Service: &orders, Worker: "FulfilOrders"}, Packages: []string{"tini"}, Extend: "docker/fulfil/Dockerfile"},
			{Of: ir.DeployableRef{Service: &db, Runner: true}, Packages: []string{"tini"}},
		},
	}}
	schema.Types["Dev"] = &ir.TypeDef{Name: "Dev", Role: ir.RoleEmbeddedStruct, Environment: &ir.EnvironmentDecl{Target: "local"}}
	source := writtenSource(t, schema, FormatTS)
	want := `images: [{ of: ShopApi, packages: ["fonts-liberation", "chromium"] }, { of: Backend, extend: "docker/backend.Dockerfile" }, ` +
		`{ of: ShopOrders, worker: "FulfilOrders", packages: ["tini"], extend: "docker/fulfil/Dockerfile" }, { of: ShopDb, runner: true, packages: ["tini"] }]`
	if !strings.Contains(source, want) {
		t.Errorf("written TypeScript lacks %s:\n%s", want, source)
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
