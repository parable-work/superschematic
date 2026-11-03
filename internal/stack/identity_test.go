package stack_test

import (
	"testing"

	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// identityOf returns the identity config field of api on server, or nil.
func identityOf(env *ir.ResolvedEnvironment, server, api string) *ir.Binding {
	for _, b := range env.Deployable(server).Bindings {
		if b.IdentityOf == api {
			return b
		}
	}
	return nil
}

// identityShop is the shop whose shop-api authenticates with the identity
// runtime (D50).
func identityShop() []stack.Service {
	services := stacktest.AcmeShop()
	service(services, "shop-api").Identity = true
	return services
}

// TestIdentityConfigField: shop-api's server binds SHOP_API_IDENTITY to the
// environment's env setting. Without one, on a platform that gives no
// identity config, the field is unbound and the server runs with the
// runtime's defaults. Orders, whose API has no user model, has no field.
func TestIdentityConfigField(t *testing.T) {
	env := mustResolve(t, assemble(t), stacktest.Shop(), identityShop(), "Staging")
	if b := identityOf(env, "shop-api", "shop-api"); b != nil {
		t.Errorf("shop-api binds %+v on a platform with no identity config and no setting", b)
	}

	s := stacktest.Shop()
	config := `{"trustedOrigins":["https://shop.example.com"]}`
	s.Environments[0].Settings = append(s.Environments[0].Settings, &ir.DeployableSettings{
		Of: stacktest.Of(stacktest.ShopAPI), Env: map[string]ir.EnvValue{"SHOP_API_IDENTITY": {Value: config}},
	})
	env = mustResolve(t, assemble(t), s, identityShop(), "Staging")
	b := identityOf(env, "shop-api", "shop-api")
	if b == nil || b.Field != "SHOP_API_IDENTITY" || b.Source != ir.BindingLiteral || b.Default || b.Value != config {
		t.Errorf("binding = %+v, want SHOP_API_IDENTITY bound to the setting", b)
	}
	if identityOf(env, "Orders", "shop-orders") != nil {
		t.Error("Orders, whose API has no user model, has an identity config field")
	}
}

// TestIdentityConfigFieldCollisions: a config field that takes the identity
// config field's name, or a name it begins with and an underscore, is
// refused.
func TestIdentityConfigFieldCollisions(t *testing.T) {
	services := identityShop()
	api := service(services, "shop-api")
	api.Config.Fields = append(api.Config.Fields, stack.ConfigField{Name: "SHOP_API_IDENTITY_TTL"})
	_, errs := resolve(t, assemble(t), stacktest.Shop(), services, "Staging")
	mustFail(t, errs, stack.CodeFieldCollision, "server shop-api: config field SHOP_API_IDENTITY_TTL of ShopApiConfig collides with SHOP_API_IDENTITY, the identity config field of shop-api")
}

// noIdentityTarget places servers on a platform that runs no identity
// runtime, as cloudflare.workers runs none (D64).
type noIdentityTarget struct{}

func (noIdentityTarget) Name() string { return "noidentity" }

func (noIdentityTarget) Register(r *registry.Registry) error {
	if err := r.RegisterPlatform(registry.PlatformSpec{
		Name: "noidentity.run", Provider: "noidentity", Kind: ir.DeployableServer,
		Languages:  []string{registry.APILanguageTypeScript},
		NoIdentity: "its isolates hash no passwords",
		NameOf:     func(ctx registry.PlatformContext) any { return ctx.Deployable.Name },
		AddressOf:  func(ctx registry.PlatformContext) any { return "https://" + ctx.Deployable.Name },
		Lower:      func(registry.PlatformContext) (registry.Lowered, error) { return registry.Lowered{}, nil },
	}); err != nil {
		return err
	}
	return r.RegisterTarget(registry.TargetSpec{
		Name:      "noidentity",
		Platforms: map[ir.DeployableKind]string{ir.DeployableServer: "noidentity.run"},
	})
}

// TestAPlatformThatRunsNoIdentityRuntime: a server of an API over the user
// model, on a platform that says it runs no identity runtime, is refused
// as unrealizable with the platform's reason; the same server of an API
// without one resolves.
func TestAPlatformThatRunsNoIdentityRuntime(t *testing.T) {
	s := &ir.Stack{
		Name:         "edge-stack",
		Deploy:       []ir.ServiceRef{stacktest.ShopStorefront},
		Environments: []*ir.Environment{{Name: "Edge", Target: "noidentity"}},
	}
	reg := assemble(t, noIdentityTarget{})
	mustResolve(t, reg, s, stacktest.AcmeShop(), "Edge")

	services := stacktest.AcmeShop()
	service(services, "shop-storefront").Identity = true
	_, errs := resolve(t, reg, s, services, "Edge")
	mustFail(t, errs, stack.CodeUnrealizable, "server shop-storefront serves shop-storefront, an API over the user model, which authenticates with the identity runtime (D50), and platform noidentity.run runs none: its isolates hash no passwords")
}

// partialIdentityTarget places servers on a platform that serves the user
// model over one of its two Postgres platforms alone, as cloudflare.workers
// serves it over cloudflare.hyperdrive and not D1 (D92): partial.pg, which
// the target places databases on, and partial.other, which a setting may.
type partialIdentityTarget struct{}

func (partialIdentityTarget) Name() string { return "partialidentity" }

func (partialIdentityTarget) Register(r *registry.Registry) error {
	lower := func(registry.PlatformContext) (registry.Lowered, error) { return registry.Lowered{}, nil }
	name := func(ctx registry.PlatformContext) any { return ctx.Deployable.Name }
	address := func(ctx registry.PlatformContext) any { return "https://" + ctx.Deployable.Name }
	if err := r.RegisterPlatform(registry.PlatformSpec{
		Name: "partial.run", Provider: "partial", Kind: ir.DeployableServer,
		Languages:         []string{registry.APILanguageTypeScript},
		NoIdentity:        "its identity store runs on partial.pg alone",
		IdentityDatabases: []string{"partial.pg"},
		NameOf:            name,
		AddressOf:         address,
		Lower:             lower,
	}); err != nil {
		return err
	}
	for _, db := range []string{"partial.pg", "partial.other"} {
		if err := r.RegisterPlatform(registry.PlatformSpec{
			Name: db, Provider: "partial", Kind: ir.DeployableDatabase,
			Dialects:  []string{registry.SQLDialectPostgres},
			NameOf:    name,
			AddressOf: address,
			Lower:     lower,
		}); err != nil {
			return err
		}
		url := "postgres://partial/" + db
		if err := r.RegisterConnector(registry.ConnectorSpec{
			Name: "partial.run-" + db, Extension: "partialidentity", Edge: ir.EdgeSQL, From: "partial.run", To: db,
			Connect: func(registry.ConnectorContext) (registry.Connected, error) {
				return registry.Connected{Value: ir.DatabaseConnection{URL: url}}, nil
			},
		}); err != nil {
			return err
		}
	}
	return r.RegisterTarget(registry.TargetSpec{
		Name:      "partialidentity",
		Platforms: map[ir.DeployableKind]string{ir.DeployableServer: "partial.run", ir.DeployableDatabase: "partial.pg"},
	})
}

// TestAPlatformThatServesTheUserModelOverSomeDatabases: a server of an API
// over the user model, on a platform that serves it over partial.pg alone
// (registry.PlatformSpec.IdentityDatabases, D92), resolves while its authDb
// is on partial.pg, and is refused with the platform's reason when a
// setting places the authDb on partial.other.
func TestAPlatformThatServesTheUserModelOverSomeDatabases(t *testing.T) {
	s, services := notesStack(registry.APILanguageTypeScript, registry.SQLDialectPostgres)
	s.Environments = []*ir.Environment{{Name: "Edge", Target: "partialidentity"}}
	service(services, "notes").Identity = true
	reg := assemble(t, partialIdentityTarget{})
	env := mustResolve(t, reg, s, services, "Edge")
	if db := env.Deployable("notes-db"); db == nil || db.Platform != "partial.pg" {
		t.Fatalf("notes-db = %+v, want it on partial.pg", db)
	}

	settingsFor(s.Environments[0], stacktest.Of(ir.ServiceRef{Name: "notes-db", Kind: ir.SchemaKindDB})).Platform = "partial.other"
	_, errs := resolve(t, reg, s, services, "Edge")
	mustFail(t, errs, stack.CodeUnrealizable, "server notes serves notes, an API over the user model, which authenticates with the identity runtime (D50) over notes-db, and environment Edge places notes-db on partial.other, where platform partial.run runs none: its identity store runs on partial.pg alone")
}
