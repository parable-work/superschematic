package tsrestgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// principalRateLimitFixture is fixture-service-auth-api with getReservation
// limited to two requests a minute per principal and syncStatus to one per
// client (D131), and an extension key keyed on releaseReservation when
// extensionKey is set.
func principalRateLimitFixture(t *testing.T, extensionKey bool) (apiFixture, error) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, serviceAuthAPI))
	if err != nil {
		t.Fatalf("load %s: %v", serviceAuthAPI, err)
	}
	two, one := 2, 1
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			switch {
			case op.Name == "getReservation":
				op.Middleware = &ir.MiddlewareConfig{RateLimit: &two, RateLimitKey: ir.RateLimitKeyPrincipal}
			case op.Name == "syncStatus":
				op.Middleware = &ir.MiddlewareConfig{RateLimit: &one, RateLimitKey: ir.RateLimitKeyIP}
			case op.Name == "releaseReservation" && extensionKey:
				op.Middleware = &ir.MiddlewareConfig{RateLimit: &one, RateLimitKey: "org"}
			}
		}
	}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:      sessionauth.Provider{},
		SchemaName:    serviceAuthAPI,
		RateLimitKeys: []apigen.RateLimitKey{{Name: "org", GoPrincipal: `func(r *gohttp.Request) string { return r.Header.Get("X-Org") }`}},
		Clock:         fixedClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	fixture := apiFixture{name: serviceAuthAPI, schema: schema, endpoints: endpoints}
	_, err = Generate(schema, endpoints, Options{SchemaName: serviceAuthAPI, Clock: fixedClock})
	return fixture, err
}

// TestGeneratedPrincipalRateLimitRouter type-checks the generated package
// for fixture-service-auth-api with a principal and an ip @rateLimit and
// drives it under bun (D131): RouterOptions requires a
// principalRateLimitStore; the principal route counts each caller the
// permission gate admits, after it, answers 429 with Retry-After past the
// limit and 503 when the store fails; the ip route counts per client.
func TestGeneratedPrincipalRateLimitRouter(t *testing.T) {
	fixture, err := principalRateLimitFixture(t, false)
	if err != nil {
		t.Fatal(err)
	}
	tree := materializeAPI(t, fixture)
	tree.typeCheck(t)
	tree.runTest(t, "principal_rate_limit_runtime.test.ts")
}

// TestTypeScriptRouterRefusesAnExtensionRateLimitKey: an extension's key
// reads a principal only its Go readers know, so the TypeScript router
// refuses it (D131).
func TestTypeScriptRouterRefusesAnExtensionRateLimitKey(t *testing.T) {
	_, err := principalRateLimitFixture(t, true)
	if err == nil || !strings.Contains(err.Error(), `@rateLimit key "org" is an extension's`) {
		t.Fatalf("err = %v, want a refusal of the extension key", err)
	}
}
