package apigen_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// orgRateLimitKey is an extension's principal key: it counts per the
// organization a request names in X-Org.
var orgRateLimitKey = apigen.RateLimitKey{
	Name:        "org",
	GoPrincipal: `func(r *gohttp.Request) string { return r.Header.Get("X-Org") }`,
}

// TestPrincipalRateLimitRoutes compiles fixture-service-auth-api as a
// public API over fixture-db with @rateLimit routes of each key kind, and
// an extension's key "org" that reads X-Org, beside a timeout and a body
// limit as a route of each shape has them, and runs
// testdata/service_auth/rate_limit_routes_test.go in it (D131): Validate
// refuses a nil PrincipalRateLimitStore; a principal route counts per
// principal after the AuthMiddleware and the permission check, answers 429
// with Retry-After past the limit, 400 to a request with no principal and
// 503 when the store fails; an ip route counts per client in process.
func TestPrincipalRateLimitRoutes(t *testing.T) {
	two, one, five := 2, 1, 5
	apiDir := buildPublicAPIWithKeys(t, sessionauth.Provider{}, serviceAuthAPI, nil, []apigen.RateLimitKey{orgRateLimitKey}, func(schema *ir.Schema) {
		// GET with a path parameter and a timeout.
		serviceAuthOperation(t, schema, "getReservation").Middleware = &ir.MiddlewareConfig{RateLimit: &two, RateLimitKey: ir.RateLimitKeyPrincipal, Timeout: &five}
		// POST with a path parameter, a permission and a service clause.
		serviceAuthOperation(t, schema, "releaseReservation").Middleware = &ir.MiddlewareConfig{RateLimit: &one, RateLimitKey: "org"}
		// GET of a list in an Authenticated set.
		serviceAuthOperation(t, schema, "listReservations").Middleware = &ir.MiddlewareConfig{RateLimit: &one, RateLimitKey: "org"}
		// POST with a body limit.
		serviceAuthOperation(t, schema, "syncMyStock").Middleware = &ir.MiddlewareConfig{RateLimit: &one, RateLimitKey: "org", BodyLimit: &one}
		serviceAuthOperation(t, schema, "syncStatus").Middleware = &ir.MiddlewareConfig{RateLimit: &one, RateLimitKey: ir.RateLimitKeyIP}
	})
	copyServiceAuthTests(t, apiDir, "rate_limit_routes_test.go")
	runGoAPIModule(t, apiDir)
}

// TestPrincipalRateLimitIsRefusedWhereItCannotCount: a principal
// @rateLimit on a route that establishes no caller is refused, since every
// request would be refused 400, and so is a key that is neither the core's
// nor an extension's (D131).
func TestPrincipalRateLimitIsRefusedWhereItCannotCount(t *testing.T) {
	one := 1
	for name, c := range map[string]struct {
		op, key, want string
	}{
		"public route":           {"syncStatus", ir.RateLimitKeyPrincipal, "counts per principal, and the route establishes no caller"},
		"extension public route": {"syncStatus", "org", "counts per principal, and the route establishes no caller"},
		"unknown key":            {"getReservation", "team", `@rateLimit key "team" is neither`},
	} {
		t.Run(name, func(t *testing.T) {
			schema, err := loader.LoadService(filepath.Join(fixturesDir, serviceAuthAPI))
			if err != nil {
				t.Fatal(err)
			}
			serviceAuthOperation(t, schema, c.op).Middleware = &ir.MiddlewareConfig{RateLimit: &one, RateLimitKey: c.key}
			_, err = apigen.Generate(schema, apigen.Options{
				Provider:      sessionauth.Provider{},
				SchemaName:    serviceAuthAPI,
				ModulePath:    "example.com/schemas/api/" + serviceAuthAPI,
				TypesModule:   "example.com/schemas/types/go/" + serviceAuthAPI,
				RateLimitKeys: []apigen.RateLimitKey{orgRateLimitKey},
				Clock:         goModuleClock,
			})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}
