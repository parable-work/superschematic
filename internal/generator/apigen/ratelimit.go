package apigen

import (
	"fmt"
	"sort"

	ir "github.com/parable-work/superschematic/ir"
)

// RateLimitKey is a @rateLimit key an extension names (D131): a principal
// key under the extension's own word for the principal its Go routers count
// per, such as an organization. Registered with
// Registry.RegisterRateLimitKey. A route with it counts per that principal
// in the router's PrincipalRateLimitStore, as a route with the core's
// "principal" key does. The Rust and TypeScript routers refuse it: only the
// core key reads a principal they know.
type RateLimitKey struct {
	// Name is the key as a schema writes it: @rateLimit({ key: Name }). It
	// is neither "ip" nor "principal".
	Name string
	// GoImport is the import path GoPrincipal needs, "" for none.
	GoImport string
	// GoImportAlias names the import in the generated file, "" for the
	// package's own name.
	GoImportAlias string
	// GoPrincipal is a Go expression of type func(*http.Request) string,
	// which the route calls for the request's principal: "" when the
	// request has none, which the route refuses 400.
	GoPrincipal string
	// Extension names the registering extension.
	Extension string
}

// Validate reports a RateLimitKey the registry cannot hold.
func (k RateLimitKey) Validate() error {
	switch k.Name {
	case "":
		return fmt.Errorf("rate limit key has no name")
	case ir.RateLimitKeyIP, ir.RateLimitKeyPrincipal:
		return fmt.Errorf("rate limit key %q is the core's", k.Name)
	}
	if k.GoPrincipal == "" {
		return fmt.Errorf("rate limit key %q has no GoPrincipal", k.Name)
	}
	return nil
}

// IsPrincipalRateLimitKey reports whether key counts per a principal: the
// core's "principal", or a key an extension registered.
func IsPrincipalRateLimitKey(key string, extensionKeys []RateLimitKey) bool {
	if key == ir.RateLimitKeyPrincipal {
		return true
	}
	_, ok := findRateLimitKey(key, extensionKeys)
	return ok
}

func findRateLimitKey(key string, extensionKeys []RateLimitKey) (RateLimitKey, bool) {
	for _, k := range extensionKeys {
		if k.Name == key {
			return k, true
		}
	}
	return RateLimitKey{}, false
}

// PrincipalRateLimited reports whether the router mounts the route with a
// principal @rateLimit (D131): one whose key counts per a principal. A
// @manualRouteRegistration operation gets no generated middleware, so its
// key has no effect there.
func (e EndpointInfo) PrincipalRateLimited() bool {
	return e.RateLimit != nil && e.RateLimitKey != "" && e.RateLimitKey != ir.RateLimitKeyIP
}

// RateLimitBucket names the route's principal counter: one per route, as
// the per-IP limiter has one per route. It is the route's method and path.
func (e EndpointInfo) RateLimitBucket() string {
	return e.Method + " " + e.Path
}

// checkPrincipalRateLimit refuses a principal @rateLimit on a route that
// establishes no caller: such a route has no principal to count, and every
// request would be refused 400.
func checkPrincipalRateLimit(e EndpointInfo) error {
	if !e.RequiresAuth {
		return fmt.Errorf("apigen: operation %s: @rateLimit key %q counts per principal, and the route establishes no caller: declare @auth, a permission or an Authenticated set", e.Name, e.RateLimitKey)
	}
	return nil
}

// goPrincipalExpr is the Go expression of the route's principal reader:
// the session principal for the core key, the extension's GoPrincipal for
// its own.
func goPrincipalExpr(key string, extensionKeys []RateLimitKey) string {
	if k, ok := findRateLimitKey(key, extensionKeys); ok {
		return k.GoPrincipal
	}
	return "runtimemiddleware.SessionPrincipal"
}

// GoImport is one import line of a generated Go file: Alias "" imports the
// package under its own name.
type GoImport struct {
	Alias string
	Path  string
}

// summarizeRateLimits sets HasPrincipalRateLimit and the imports of the
// routes' principal readers, and refuses a key that is neither the core's
// nor an extension's, and a principal key on a route with no caller.
func (o *APIOutput) summarizeRateLimits() error {
	o.HasPrincipalRateLimit, o.PrincipalRateLimitImports = false, nil
	imports := map[string]GoImport{}
	for _, endpoint := range o.Endpoints {
		if endpoint.RateLimit == nil {
			continue
		}
		key := endpoint.RateLimitKey
		if key != "" && key != ir.RateLimitKeyIP && !IsPrincipalRateLimitKey(key, o.RateLimitKeys) {
			return fmt.Errorf("apigen: operation %s: @rateLimit key %q is neither %q, %q nor a key an extension registers", endpoint.Name, key, ir.RateLimitKeyIP, ir.RateLimitKeyPrincipal)
		}
		if !endpoint.PrincipalRateLimited() {
			continue
		}
		if err := checkPrincipalRateLimit(endpoint); err != nil {
			return err
		}
		o.HasPrincipalRateLimit = true
		if k, ok := findRateLimitKey(key, o.RateLimitKeys); ok && k.GoImport != "" {
			imports[k.GoImport] = GoImport{Alias: k.GoImportAlias, Path: k.GoImport}
		}
	}
	for _, imp := range imports {
		o.PrincipalRateLimitImports = append(o.PrincipalRateLimitImports, imp)
	}
	sort.Slice(o.PrincipalRateLimitImports, func(i, j int) bool {
		return o.PrincipalRateLimitImports[i].Path < o.PrincipalRateLimitImports[j].Path
	})
	return nil
}

// PrincipalReader is the Go expression of the route's principal reader
// (D131).
func (d routeData) PrincipalReader() string {
	return goPrincipalExpr(d.RateLimitKey, d.Output.RateLimitKeys)
}
