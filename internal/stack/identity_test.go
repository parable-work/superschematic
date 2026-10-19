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
