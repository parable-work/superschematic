package stack

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// The identity config of an API over the user model (D50). A served API
// whose server authenticates with the identity runtime (Service.Identity)
// gets an identity config field, named by ir.IdentityConfigField, whose
// value is the runtime's config as JSON: the session's lifetime, the
// cookie, the trusted origins and the password hash's cost. The
// environment sets it through its env settings, a literal or a parameter
// like any config field; without one the server's platform supplies its
// identity config (registry.PlatformSpec.IdentityConfig), as the local
// platform turns the cookie's Secure off over plain HTTP; without either
// the field is unbound and the server runs with the runtime's defaults.

// checkIdentityDatabases refuses a server or an app on a platform that
// serves the user model over some database platforms alone
// (registry.PlatformSpec.IdentityDatabases, D92) when it serves an API over
// the user model whose authDb the environment places on another, giving
// the platform's NoIdentity reason. It runs once every deployable is
// placed.
func (r *resolver) checkIdentityDatabases() {
	for _, name := range slices.Sorted(maps.Keys(r.deployables)) {
		d := r.deployables[name]
		if d.platform.NoIdentity == "" || len(d.platform.IdentityDatabases) == 0 {
			continue
		}
		if d.res.Kind != ir.DeployableServer && d.res.Kind != ir.DeployableApp {
			continue
		}
		for _, served := range d.res.Services {
			svc := r.services[served.Name]
			if svc == nil || !svc.Identity {
				continue
			}
			db := identityDatabaseOf(svc)
			if db == "" {
				continue // databaseOf refuses an API with no one database
			}
			host, ok := r.deployables[r.byService[db]]
			if !ok || host.res.Platform == "" || slices.Contains(d.platform.IdentityDatabases, host.res.Platform) {
				continue // a database no platform placed is refused already
			}
			r.fail(CodeUnrealizable, "%s %s serves %s, an API over the user model, which authenticates with the identity runtime (D50) over %s, and environment %s places %s on %s, where platform %s runs none: %s",
				d.res.Kind, d.res.Name, svc.Name, db, r.envName(), db, host.res.Platform, d.platform.Name, d.platform.NoIdentity)
		}
	}
}

// identityDatabaseOf is the database an API over the user model keeps its
// users in: its authDb, else its one DB dependency, else none (databaseOf,
// which reports the ambiguity, without reporting it again).
func identityDatabaseOf(svc *Service) string {
	if svc.AuthDB != nil {
		return svc.AuthDB.Name
	}
	db := ""
	for _, dep := range svc.Dependencies {
		if dep.Kind != ir.SchemaKindDB {
			continue
		}
		if db != "" {
			return ""
		}
		db = dep.Name
	}
	return db
}

// identityFields returns the identity config fields of server d: one per
// API it serves whose server authenticates with the identity runtime, by
// field name.
func (r *resolver) identityFields(d *deployable) map[string]string {
	fields := map[string]string{}
	for _, served := range d.res.Services {
		if svc := r.services[served.Name]; svc != nil && svc.Identity {
			fields[ir.IdentityConfigField(svc.Name)] = svc.Name
		}
	}
	return fields
}

// checkIdentityFields refuses an identity config field of server or app d (D78) that a
// config field, an edge's derived field or a callers field takes, by its
// name or a name it begins with and an underscore, either way round.
func (r *resolver) checkIdentityFields(d *deployable, identity, callers map[string]string, derived map[string]*edge) {
	for _, name := range slices.Sorted(maps.Keys(identity)) {
		api := identity[name]
		claims := func(other string) bool {
			return ir.DerivedFieldClaims(name, other) || ir.DerivedFieldClaims(other, name)
		}
		for _, fieldName := range slices.Sorted(maps.Keys(d.fields)) {
			if f := d.fields[fieldName]; claims(f.name) {
				r.fail(CodeFieldCollision, "%s %s: config field %s of %s collides with %s, the identity config field of %s", d.res.Kind, d.res.Name, f.name, f.declaring, name, api)
			}
		}
		for _, other := range slices.Sorted(maps.Keys(derived)) {
			if claims(other) {
				r.fail(CodeFieldCollision, "%s %s: %s, the field edge %s derives, collides with %s, the identity config field of %s", d.res.Kind, d.res.Name, other, derived[other].res.ID, name, api)
			}
		}
		for _, other := range slices.Sorted(maps.Keys(callers)) {
			if claims(other) {
				r.fail(CodeFieldCollision, "%s %s: %s, the callers field of %s, collides with %s, the identity config field of %s", d.res.Kind, d.res.Name, other, callers[other], name, api)
			}
		}
	}
}

// identityBindings binds each identity config field of server or app d: to the
// environment's env setting when it sets one, otherwise to the platform's
// identity config as a default literal, otherwise not at all. For an API with
// @userSessions({ oauth }) (#695), it populates oauth.publicBaseUrl from the
// API's site origin (or exposed public address) unless already set explicitly.
func (r *resolver) identityBindings(d *deployable, identity map[string]string) []*ir.Binding {
	var out []*ir.Binding
	for _, name := range slices.Sorted(maps.Keys(identity)) {
		api := identity[name]
		f := &field{name: name, declaring: "the identity config of " + api}
		if def := d.platform.IdentityConfig; def != "" {
			f.def = &def
		}
		b := r.bindField(d, f)
		if svc := r.services[api]; svc != nil && svc.OAuth {
			b = r.withOAuthPublicBaseURL(d, name, api, b)
		}
		if b != nil {
			b.IdentityOf = api
			out = append(out, b)
		}
	}
	return out
}

// withOAuthPublicBaseURL merges oauth.publicBaseUrl into an OAuth API's
// identity config binding from its single site origin (or its server's own
// exposed public address when no site origin serves it), preserving any
// existing settings and leaving an explicit oauth.publicBaseUrl or parameter
// binding untouched (#695).
func (r *resolver) withOAuthPublicBaseURL(d *deployable, fieldName, api string, b *ir.Binding) *ir.Binding {
	if b != nil && b.Source != ir.BindingLiteral {
		return b
	}
	baseURL := r.oauthPublicBaseURL(d, api)
	if baseURL == nil || !isValidOAuthBaseURL(baseURL) {
		return b
	}
	obj := map[string]any{}
	if b != nil {
		raw, ok := b.Value.(string)
		if !ok {
			return b
		}
		if err := json.Unmarshal([]byte(raw), &obj); err != nil || obj == nil {
			return b
		}
		if oauthMap, ok := obj["oauth"].(map[string]any); ok {
			if existing, ok := oauthMap["publicBaseUrl"].(string); ok && strings.TrimSpace(existing) != "" {
				return b
			}
		}
	}
	oauthMap, ok := obj["oauth"].(map[string]any)
	if !ok || oauthMap == nil {
		oauthMap = map[string]any{}
		obj["oauth"] = oauthMap
	}
	val, err := jsonWithOAuthBaseURL(obj, oauthMap, baseURL)
	if err != nil {
		return b
	}
	val = r.normalize(fmt.Sprintf("the identity config of %s holds", api), val)
	if val == nil {
		return b
	}
	if b == nil {
		return &ir.Binding{Field: fieldName, Source: ir.BindingLiteral, Value: val, Default: true, IdentityOf: api}
	}
	b.Value = val
	return b
}

// oauthPublicBaseURL returns the public base URL expression for api on d:
// its site origin URL when exactly one site serves it on its origin, else d's
// own exposed public address when no site origin serves it, else nil.
func (r *resolver) oauthPublicBaseURL(d *deployable, api string) any {
	var originURLs []any
	for _, siteName := range d.res.Origins {
		site := r.deployables[siteName]
		if site == nil || site.res.Site == nil || site.res.Site.OriginAPI(api) == nil || site.res.PublicAddress == nil {
			continue
		}
		u := r.normalize(fmt.Sprintf("the OAuth public base URL of %s on site %s is", api, siteName), ir.SiteOriginURL(site.res.PublicAddress, api))
		if u != nil {
			originURLs = append(originURLs, u)
		}
	}
	switch len(originURLs) {
	case 1:
		return originURLs[0]
	case 0:
		if d.res.Exposed && d.res.PublicAddress != nil {
			return deepCopy(d.res.PublicAddress)
		}
		return nil
	default:
		return nil
	}
}

// isValidOAuthBaseURL reports whether v produces a URL accepted as
// oauth.publicBaseUrl: https, http on loopback, or an output/parameter
// expression without a non-loopback http:// prefix.
func isValidOAuthBaseURL(v any) bool {
	switch v := v.(type) {
	case string:
		return isValidOAuthURLString(v)
	case ir.Concat:
		if len(v) == 0 {
			return false
		}
		if prefix, ok := v[0].(string); ok {
			return isValidOAuthURLPrefix(prefix)
		}
		return true
	case ir.Output, ir.Parameter:
		return true
	default:
		return false
	}
}

func isValidOAuthURLString(s string) bool {
	if strings.HasPrefix(s, "https://") {
		return len(s) > len("https://")
	}
	return isLoopbackHTTPPrefix(s)
}

func isValidOAuthURLPrefix(prefix string) bool {
	if strings.HasPrefix(prefix, "https://") {
		return true
	}
	return isLoopbackHTTPPrefix(prefix)
}

func isLoopbackHTTPPrefix(s string) bool {
	for _, host := range []string{"http://localhost", "http://127.0.0.1", "http://[::1]"} {
		if s == host || strings.HasPrefix(s, host+":") || strings.HasPrefix(s, host+"/") {
			return true
		}
	}
	return false
}

// jsonWithOAuthBaseURL encodes obj as JSON with oauthMap["publicBaseUrl"] set
// to baseURL, returning a string for a literal baseURL or an ir.Concat splicing
// any ir.Parameter / ir.Output references inside the JSON string literal.
func jsonWithOAuthBaseURL(obj, oauthMap map[string]any, baseURL any) (any, error) {
	if s, ok := baseURL.(string); ok {
		oauthMap["publicBaseUrl"] = strings.TrimRight(s, "/")
		data, err := json.Marshal(obj)
		if err != nil {
			return nil, err
		}
		return string(data), nil
	}
	sentinel := "__SUPERSCHEMATIC_OAUTH_BASE_URL__"
	for {
		oauthMap["publicBaseUrl"] = sentinel
		data, err := json.Marshal(obj)
		if err != nil {
			return nil, err
		}
		raw := string(data)
		if strings.Count(raw, sentinel) == 1 {
			before, after, _ := strings.Cut(raw, sentinel)
			pieces := []any{baseURL}
			if c, ok := baseURL.(ir.Concat); ok {
				pieces = c
			}
			parts := []any{before}
			for i, piece := range pieces {
				switch p := piece.(type) {
				case string:
					if i == len(pieces)-1 {
						p = strings.TrimRight(p, "/")
					}
					quoted, err := json.Marshal(p)
					if err != nil {
						return nil, err
					}
					parts = append(parts, string(quoted[1:len(quoted)-1]))
				case ir.Parameter, ir.Output:
					parts = append(parts, p)
				default:
					return nil, fmt.Errorf("unexpected piece %T in oauth.publicBaseUrl", piece)
				}
			}
			parts = append(parts, after)
			return ir.Concat(parts), nil
		}
		sentinel += "_"
	}
}
