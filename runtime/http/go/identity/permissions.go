package identity

import (
	"github.com/parable-work/superschematic/runtime/http/go/session"
)

// ValidPermission reports whether p has a permission's form: dotted
// segments of ASCII letters, digits, '_' and '-', none empty
// ("orders.read", "identity.users.write").
func ValidPermission(p string) bool {
	if p == "" {
		return false
	}
	segment := 0
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '.':
			if segment == 0 {
				return false
			}
			segment = 0
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
			segment++
		default:
			return false
		}
	}
	return segment > 0
}

// EffectivePermissions are the permissions roles carry, without repeats:
// each role's in its order, the roles in the order given, and a permission
// kept where it first appears. A permission another covers is still
// listed ("a" and "a.b" both are).
func EffectivePermissions(roles []session.Role) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, role := range roles {
		for _, p := range role.Permissions {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// Uncovered lists the permissions of given that no permission of held
// covers (session.Covers), in given's order and without repeats. A caller
// may write a role with given, or grant a role carrying it, only when it
// is empty: no one grants what they do not hold.
func Uncovered(held, given []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, p := range given {
		if seen[p] {
			continue
		}
		seen[p] = true
		covered := false
		for _, h := range held {
			if session.Covers(h, p) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, p)
		}
	}
	return out
}

// Route is what capabilities needs to know of one operation of the API,
// the route requirements the generated server passes.
type Route struct {
	// OperationID is the operation's OpenAPI operation id, its key in
	// Capabilities.
	OperationID string `json:"operationId"`
	// RequiresAuth: the route needs a caller (@auth).
	RequiresAuth bool `json:"requiresAuth"`
	// Permissions are its @requirePermission list. A route with any needs
	// a caller.
	Permissions []string `json:"permissions"`
	// RequireOwnership is @requireOwnership: the route needs a caller,
	// and whether they own a resource is the implementation's, so
	// capabilities answers only whether the caller is admitted.
	RequireOwnership bool `json:"requireOwnership"`
	// ServiceOnly is @requireService: only a service calls it, so
	// capabilities leaves it out.
	ServiceOnly bool `json:"serviceOnly"`
}

// needsCaller reports whether the route admits only an authenticated
// caller.
func (r Route) needsCaller() bool {
	return r.RequiresAuth || len(r.Permissions) > 0 || r.RequireOwnership
}

// Admits reports whether the route admits a caller with roles, by the
// router's rule: a route that needs a caller admits none when
// authenticated is false, and one with permissions admits the caller when
// matcher accepts the roles (session.AnyRoleCoversAny when nil: any listed
// permission covered by any permission held).
func (r Route) Admits(authenticated bool, roles []session.Role, matcher session.Matcher) bool {
	if r.needsCaller() && !authenticated {
		return false
	}
	if len(r.Permissions) == 0 {
		return true
	}
	if matcher == nil {
		matcher = session.AnyRoleCoversAny
	}
	return matcher(roles, r.Permissions)
}

// CapabilitiesOf answers, for each route an end user may call (every route
// but a service-only one), whether it admits an authenticated caller with
// roles, keyed by operation id.
func CapabilitiesOf(routes []Route, roles []session.Role, matcher session.Matcher) map[string]bool {
	out := make(map[string]bool, len(routes))
	for _, r := range routes {
		if r.ServiceOnly {
			continue
		}
		out[r.OperationID] = r.Admits(true, roles, matcher)
	}
	return out
}
