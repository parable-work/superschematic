package stack_test

import (
	"testing"

	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// minClientVersionsOf returns the binding of MIN_CLIENT_VERSIONS on a
// deployable, or nil.
func minClientVersionsOf(env *ir.ResolvedEnvironment, deployable string) *ir.Binding {
	for _, b := range env.Deployable(deployable).Bindings {
		if b.Field == ir.MinClientVersionsSetting {
			return b
		}
	}
	return nil
}

// TestClientVersionsSetting: the server of an API whose config says
// clientVersions takes MIN_CLIENT_VERSIONS from the environment's env
// (D143), and is unbound without one, since the setting is optional. A
// server whose APIs do not say so refuses the key as any unknown one, and
// an @envVars field of that name collides with it.
func TestClientVersionsSetting(t *testing.T) {
	versioned := func() []stack.Service {
		services := stacktest.AcmeShop()
		service(services, "shop-api").ClientVersions = true
		return services
	}
	env := mustResolve(t, assemble(t), stacktest.Shop(), versioned(), "Staging")
	if b := minClientVersionsOf(env, "shop-api"); b != nil {
		t.Errorf("shop-api binds %+v with no setting", b)
	}

	withSetting := func() *ir.Stack {
		s := stacktest.Shop()
		s.Environments[0].Settings = append(s.Environments[0].Settings, &ir.DeployableSettings{
			Of: stacktest.Of(stacktest.ShopAPI), Env: map[string]ir.EnvValue{ir.MinClientVersionsSetting: {Value: "cli=0.5.0,desktop=2.1.0"}},
		})
		return s
	}
	env = mustResolve(t, assemble(t), withSetting(), versioned(), "Staging")
	b := minClientVersionsOf(env, "shop-api")
	if b == nil || b.Source != ir.BindingLiteral || b.Default || b.Value != "cli=0.5.0,desktop=2.1.0" {
		t.Errorf("binding = %+v, want MIN_CLIENT_VERSIONS bound to the setting", b)
	}
	if minClientVersionsOf(env, "Orders") != nil {
		t.Error("Orders, whose API does not say clientVersions, binds MIN_CLIENT_VERSIONS")
	}

	// Without clientVersions the key is no field of the server.
	_, errs := resolve(t, assemble(t), withSetting(), stacktest.AcmeShop(), "Staging")
	mustFail(t, errs, stack.CodeUnknownEnvKey, "sets env MIN_CLIENT_VERSIONS on server shop-api, which is not a field of")

	// An @envVars field of the same name is another field.
	services := versioned()
	api := service(services, "shop-api")
	api.Config.Fields = append(api.Config.Fields, stack.ConfigField{Name: ir.MinClientVersionsSetting})
	_, errs = resolve(t, assemble(t), stacktest.Shop(), services, "Staging")
	mustFail(t, errs, stack.CodeFieldCollision, "server shop-api: config field MIN_CLIENT_VERSIONS is declared by both the setting of an API whose config says clientVersions (service shop-api) and ShopApiConfig (service shop-api)")
}
