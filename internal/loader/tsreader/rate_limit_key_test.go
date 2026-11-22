package tsreader

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// rateLimitSource is an API with one @rateLimit operation whose config is
// cfg, and extra above the class.
func rateLimitSource(cfg, extra string) string {
	return `import { HttpMethod, auth, rateLimit, rest } from "@superschematic/api";
` + extra + `
export class OrderMutations {
  @auth
  @rateLimit(` + cfg + `)
  @rest(HttpMethod.POST, "orders")
  placeOrder(sku: string): string {
    throw new Error("schema declaration only");
  }
}
`
}

func loadRateLimitKey(t *testing.T, reg *registry.Registry, cfg, extra string) (*ir.MiddlewareConfig, error) {
	t.Helper()
	dir := decoratorTestService(t, "API", map[string]string{"src/a.schema.ts": rateLimitSource(cfg, extra)})
	var opts []Option
	if reg != nil {
		opts = append(opts, WithRegistry(reg))
	}
	schema, _, _, err := LoadServiceWithConfig(dir, opts...)
	if err != nil {
		return nil, err
	}
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			if op.Name == "placeOrder" {
				return op.Middleware, nil
			}
		}
	}
	t.Fatal("placeOrder not loaded")
	return nil, nil
}

// TestRateLimitKeyLoads (D131): @rateLimit takes key 'ip' or 'principal',
// and an absent key leaves it empty, which counts per client IP.
func TestRateLimitKeyLoads(t *testing.T) {
	for cfg, want := range map[string]string{
		"{ requestsPerMinute: 60 }":                   "",
		"{ requestsPerMinute: 60, key: 'ip' }":        ir.RateLimitKeyIP,
		"{ requestsPerMinute: 60, key: 'principal' }": ir.RateLimitKeyPrincipal,
	} {
		mw, err := loadRateLimitKey(t, nil, cfg, "")
		if err != nil {
			t.Fatalf("%s: %v", cfg, err)
		}
		if mw == nil || mw.RateLimit == nil || *mw.RateLimit != 60 || mw.RateLimitKey != want {
			t.Errorf("%s: middleware = %+v, want 60 per minute keyed %q", cfg, mw, want)
		}
	}
}

// TestRateLimitKeyRefusesAnUnknownKey: a key that is neither the core's
// nor a registered one is refused, by the TypeScript checker first.
func TestRateLimitKeyRefusesAnUnknownKey(t *testing.T) {
	_, err := loadRateLimitKey(t, nil, "{ requestsPerMinute: 60, key: 'team' }", "")
	if err == nil || !strings.Contains(err.Error(), `"team"`) {
		t.Fatalf("err = %v, want a refusal of key \"team\"", err)
	}
}

// TestRateLimitKeyTakesAnExtensionKey: a key an extension registers loads
// as written, where the extension's declaration merging adds it to
// RateLimitKeys, and without the registration the loader refuses it.
func TestRateLimitKeyTakesAnExtensionKey(t *testing.T) {
	const merge = `declare module "@superschematic/api" {
  interface RateLimitKeys {
    readonly org: true;
  }
}`
	reg := registry.New(naming.Default())
	if err := reg.RegisterRateLimitKey(apigen.RateLimitKey{Name: "org", GoPrincipal: `func(r *gohttp.Request) string { return r.Header.Get("X-Org") }`}); err != nil {
		t.Fatal(err)
	}
	mw, err := loadRateLimitKey(t, reg, "{ requestsPerMinute: 5, key: 'org' }", merge)
	if err != nil {
		t.Fatal(err)
	}
	if mw.RateLimitKey != "org" {
		t.Fatalf("key = %q, want org", mw.RateLimitKey)
	}
	if _, err := loadRateLimitKey(t, nil, "{ requestsPerMinute: 5, key: 'org' }", merge); err == nil || !strings.Contains(err.Error(), "a key an extension registers") {
		t.Fatalf("unregistered key: err = %v", err)
	}
}

// TestRegisterRateLimitKeyRefusals: the registry refuses a key with no
// name, one of the core's names, one with no Go principal reader, and a
// second key of the same name.
func TestRegisterRateLimitKeyRefusals(t *testing.T) {
	reader := `func(r *gohttp.Request) string { return "" }`
	reg := registry.New(naming.Default())
	for _, k := range []apigen.RateLimitKey{
		{GoPrincipal: reader},
		{Name: "ip", GoPrincipal: reader},
		{Name: "principal", GoPrincipal: reader},
		{Name: "org"},
	} {
		if err := reg.RegisterRateLimitKey(k); err == nil {
			t.Errorf("registered %+v", k)
		}
	}
	if err := reg.RegisterRateLimitKey(apigen.RateLimitKey{Name: "org", GoPrincipal: reader}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterRateLimitKey(apigen.RateLimitKey{Name: "org", GoPrincipal: reader}); err == nil {
		t.Error("registered org twice")
	}
}
