package stack_test

import (
	"testing"

	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
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
