package stack_test

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// providerShop is the shop whose shop-api names a channel adapter in its
// slot SMS (D159), from two SMS relays of two shapes: relay-a reads a URL
// and a token, relay-b an account as well. With notices, shop-api uses a
// feature that needs a channel adapter. env sets each environment's env of
// shop-api.
func providerShop(notices bool, env map[string]map[string]ir.EnvValue) (*ir.Stack, []stack.Service) {
	s, services := shop()
	api := service(services, "shop-api")
	api.ProviderSlots = map[string]string{"SMS": ir.RuntimeChannel, "VAULT": ir.RuntimeCredentials}
	api.Providers = []*ir.RuntimeProvider{
		{Name: "relay-a", Implements: ir.RuntimeChannel, Package: "@example/relay-a", Operations: []string{"send", "health"}, Config: []ir.ProviderConfigKey{{Key: "TOKEN", Secret: true}, {Key: "URL"}}},
		{Name: "relay-b", Implements: ir.RuntimeChannel, Package: "@example/relay-b", Operations: []string{"send", "health"}, Config: []ir.ProviderConfigKey{{Key: "ACCOUNT"}, {Key: "SENDER", Optional: true}, {Key: "TOKEN", Secret: true}, {Key: "URL"}}},
		{Name: "vault", Implements: ir.RuntimeCredentials, Package: "@example/vault", Operations: []string{"list", "resolve"}},
	}
	api.Config.Fields = append(api.Config.Fields,
		stack.ConfigField{Name: "SMS"},
		stack.ConfigField{Name: "SMS_URL"},
		stack.ConfigField{Name: "SMS_ACCOUNT"},
		stack.ConfigField{Name: "SMS_TOKEN", Secret: true},
		stack.ConfigField{Name: "VAULT"},
	)
	if notices {
		api.Features = []ir.RuntimeFeatureUse{{Feature: "notices", Interface: ir.RuntimeChannel}}
	}
	for name, values := range env {
		settings := settingsFor(s.Environment(name), stacktest.Of(stacktest.ShopAPI))
		if settings.Env == nil {
			settings.Env = map[string]ir.EnvValue{}
		}
		for key, value := range values {
			settings.Env[key] = value
		}
	}
	return s, services
}

func literal(v string) ir.EnvValue { return ir.EnvValue{Value: v} }

func providersOf(env *ir.ResolvedEnvironment, deployable string) string {
	var got []string
	for _, p := range env.Deployable(deployable).Providers {
		got = append(got, p.Slot+"="+p.Provider+"("+p.Interface+","+p.Package+","+p.Service+")")
	}
	return strings.Join(got, " ")
}

// TestAnEnvironmentNamesTheProviderOfEachSlot (D159): each environment
// binds shop-api's slot SMS to a provider and its settings, and the
// resolved deployable lists the provider. Staging and Production use two
// relays, and only their env differs.
func TestAnEnvironmentNamesTheProviderOfEachSlot(t *testing.T) {
	s, services := providerShop(true, map[string]map[string]ir.EnvValue{
		"Staging":    {"SMS": literal("relay-a"), "SMS_URL": literal("http://relay-a.test")},
		"Production": {"SMS": literal("relay-b"), "SMS_URL": literal("https://relay-b.test"), "SMS_ACCOUNT": literal("AC1"), "VAULT": literal("vault")},
	})
	reg := assemble(t)
	if got := providersOf(mustResolve(t, reg, s, services, "Staging"), "shop-api"); got != "SMS=relay-a(channel,@example/relay-a,shop-api)" {
		t.Errorf("Staging's shop-api uses %s", got)
	}
	if got := providersOf(mustResolve(t, reg, s, services, "Production"), "shop-api"); got != "SMS=relay-b(channel,@example/relay-b,shop-api) VAULT=vault(credentials,@example/vault,shop-api)" {
		t.Errorf("Production's shop-api uses %s", got)
	}
}

// TestAFeatureNeedsAProviderOfItsInterface (D159): shop-api uses a
// feature that needs a channel adapter, so an environment that runs it
// with SMS unset is refused, naming the feature, the interface, the slot
// and the providers that fill it. Without the feature, the same
// environment resolves with no provider.
func TestAFeatureNeedsAProviderOfItsInterface(t *testing.T) {
	s, services := providerShop(true, nil)
	_, errs := resolve(t, assemble(t), s, services, "Staging")
	mustFail(t, errs, stack.CodeNoProvider, "environment Staging runs server shop-api, whose API shop-api uses notices, which needs a channel adapter, and the environment configures none: set SMS to one of relay-a, relay-b in its env")

	s, services = providerShop(true, nil)
	service(services, "shop-api").ProviderSlots = nil
	_, errs = resolve(t, assemble(t), s, services, "Staging")
	mustFail(t, errs, stack.CodeNoProvider, "uses notices, which needs a channel adapter", "declare a slot of channel with @providers on its @envVars class")

	s, services = providerShop(false, nil)
	if got := providersOf(mustResolve(t, assemble(t), s, services, "Staging"), "shop-api"); got != "" {
		t.Errorf("shop-api with no feature and no slot set uses %s", got)
	}
}

// TestAProviderSlotIsCheckedAgainstTheProvider (D159): a slot names a
// provider its API lists, of the slot's interface, by a literal, and the
// environment binds every setting the provider needs, a secret one to a
// secret field.
func TestAProviderSlotIsCheckedAgainstTheProvider(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]ir.EnvValue
		edit func([]stack.Service)
		want string
	}{
		"an unknown provider": {
			env:  map[string]ir.EnvValue{"SMS": literal("relay-c")},
			want: `provider slot SMS of shop-api names provider "relay-c", which is none of the providers shop-api lists (relay-a, relay-b, vault)`,
		},
		"another interface": {
			env:  map[string]ir.EnvValue{"SMS": literal("vault")},
			want: "provider slot SMS of shop-api takes a channel adapter, and provider vault (@example/vault) implements credentials; the slot names one of relay-a, relay-b",
		},
		"a setting unbound": {
			env:  map[string]ir.EnvValue{"SMS": literal("relay-b"), "SMS_URL": literal("u")},
			want: "names provider relay-b, which needs setting ACCOUNT, and the environment binds no value to SMS_ACCOUNT",
		},
		"a setting the config lacks": {
			env:  map[string]ir.EnvValue{"SMS": literal("relay-a"), "SMS_URL": literal("u")},
			edit: func(services []stack.Service) { dropField(service(services, "shop-api"), "SMS_TOKEN") },
			want: "names provider relay-a, which reads setting TOKEN from config field SMS_TOKEN, and shop-api declares no such field; declare SMS_TOKEN?: Secret<string>",
		},
		"a secret held plainly": {
			env: map[string]ir.EnvValue{"SMS": literal("relay-a"), "SMS_URL": literal("u")},
			edit: func(services []stack.Service) {
				api := service(services, "shop-api")
				dropField(api, "SMS_TOKEN")
				api.Config.Fields = append(api.Config.Fields, stack.ConfigField{Name: "SMS_TOKEN"})
			},
			want: "names provider relay-a, whose setting TOKEN is a secret, and config field SMS_TOKEN of shop-api is no secret",
		},
		"a parameter": {
			env:  map[string]ir.EnvValue{"SMS": {Parameter: "pr"}},
			want: "provider slot SMS of shop-api is bound to parameter pr; a slot takes a literal",
		},
	} {
		t.Run(name, func(t *testing.T) {
			envName := "Staging"
			if tc.env["SMS"].Parameter != "" {
				envName = "Preview"
			}
			s, services := providerShop(false, map[string]map[string]ir.EnvValue{envName: tc.env})
			if tc.edit != nil {
				tc.edit(services)
			}
			_, errs := resolve(t, assemble(t), s, services, envName)
			mustFail(t, errs, stack.CodeProvider, tc.want)
		})
	}
}

func dropField(svc *stack.Service, name string) {
	fields := svc.Config.Fields[:0]
	for _, f := range svc.Config.Fields {
		if f.Name != name {
			fields = append(fields, f)
		}
	}
	svc.Config.Fields = fields
}
