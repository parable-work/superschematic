package session

import (
	"context"
	"net/http"

	"github.com/parable-work/superschematic/runtime/http/go/response"
)

// BearerToken extracts the bearer token from the Authorization header.
// Returns the token and true when the header carries a non-empty
// "Bearer <token>" value, or ("", false) otherwise.
func BearerToken(h http.Header) (string, bool) {
	v := h.Get("Authorization")
	if len(v) > 7 && v[:7] == "Bearer " {
		if token := v[7:]; token != "" {
			return token, true
		}
	}
	return "", false
}

// Guard is the middleware skeleton every permission check shares: 401
// Unauthorized when authenticated reports false, 403 Forbidden when allowed
// reports false, otherwise the next handler. A nil allowed only checks
// authentication. Projects with their own identity and permission types
// build their middlewares on it so the two responses stay the same
// everywhere.
func Guard(authenticated func(context.Context) bool, allowed func(context.Context) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			if !authenticated(ctx) {
				response.Error(w, http.StatusUnauthorized, "Authentication required")
				return
			}
			if allowed != nil && !allowed(ctx) {
				response.Error(w, http.StatusForbidden, "Insufficient permissions")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAuth is a middleware that requires an authenticated principal.
func RequireAuth(next http.Handler) http.Handler {
	return Guard(HasPrincipalID, nil)(next)
}

// Matcher decides whether the principal's roles satisfy the required
// permissions of a route.
type Matcher func(roles []Role, required []string) bool

// RequirePermissions is a middleware that requires the principal's roles to
// cover any of permissions under the package's hierarchical rule
// (AnyRoleCoversAny). 401 when unauthenticated, 403 when not covered.
func RequirePermissions(permissions ...string) func(http.Handler) http.Handler {
	return RequirePermissionsWith(AnyRoleCoversAny, permissions...)
}

// RequirePermissionsWith is RequirePermissions with the caller's matcher in
// place of AnyRoleCoversAny.
func RequirePermissionsWith(matcher Matcher, permissions ...string) func(http.Handler) http.Handler {
	return Guard(HasPrincipalID, func(ctx context.Context) bool {
		return matcher(GetRoles(ctx), permissions)
	})
}
