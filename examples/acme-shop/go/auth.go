package shop

import (
	"context"
	"errors"
	"net/http"
	"time"

	shopapi "example.com/acme/api/shop-api"
	"github.com/parable-work/superschematic/runtime/http/go/session"
)

// TokenValidator checks a bearer token's signature and expiry and returns
// its JWT id (the jti claim). Your identity provider supplies it.
type TokenValidator func(ctx context.Context, token string) (jti string, err error)

// Auth resolves a bearer token to a principal and its roles. Sessions and
// Principals are the ORM-backed stores shop-api generates from shop-db's
// Session and User tables; Roles is yours, since shop-db has no role table.
type Auth struct {
	Validate   TokenValidator
	Sessions   session.Store
	Principals session.PrincipalStore
	Roles      session.RoleStore
}

var errSessionEnded = errors.New("session ended")

// Middleware is shopapi.Config.AuthMiddleware. It puts the caller on the
// request context and answers 401 when there is none; each route then checks
// its @requirePermission list against the caller's roles.
func (a Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if token, ok := session.BearerToken(r.Header); ok {
			if principal, roles, err := a.resolve(ctx, token); err == nil {
				ctx = shopapi.ContextWithPrincipalID(ctx, principal.ID)
				ctx = shopapi.ContextWithPrincipalName(ctx, principal.Name)
				ctx = shopapi.ContextWithRoles(ctx, roles)
			}
		}
		session.RequireAuth(next).ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a Auth) resolve(ctx context.Context, token string) (session.Principal, []session.Role, error) {
	jti, err := a.Validate(ctx, token)
	if err != nil {
		return session.Principal{}, nil, err
	}
	record, err := a.Sessions.FindByJTI(ctx, jti)
	if err != nil {
		return session.Principal{}, nil, err
	}
	if record.DeletedAt != nil || !record.ExpiresAt.After(time.Now()) {
		return session.Principal{}, nil, errSessionEnded
	}
	principal, err := a.Principals.GetByID(ctx, record.PrincipalID)
	if err != nil {
		return session.Principal{}, nil, err
	}
	roles, err := a.Roles.ListRolesForPrincipal(ctx, principal.ID)
	if err != nil {
		return session.Principal{}, nil, err
	}
	return principal, roles, nil
}
