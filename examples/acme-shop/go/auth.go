// Package shop holds what the shop's two Go APIs share: how a caller is
// authenticated. Each API's implementation is a package of its own, at the
// path the naming file's [implementation_paths] go template gives,
// go/{service}: go/shop-api and go/shop-orders. The servers the stack runs
// build each from its Deps.
package shop

import (
	"context"
	"errors"
	"net/http"
	"time"

	scalars "github.com/parable-work/superscalar/go"
	"github.com/parable-work/superschematic/runtime/http/go/session"
)

// TokenValidator checks a bearer token's signature and expiry and returns
// its JWT id (the jti claim). Your identity provider supplies it.
type TokenValidator func(ctx context.Context, token string) (jti string, err error)

// Auth resolves a bearer token to a principal and its roles. Sessions and
// Principals are the ORM-backed stores each API generates from shop-db's
// Session and User tables; Roles is yours, since shop-db has no role table.
type Auth struct {
	Validate   TokenValidator
	Sessions   session.Store
	Principals session.PrincipalStore
	Roles      session.RoleStore
}

var errSessionEnded = errors.New("session ended")

// Middleware is the generated Config.AuthMiddleware. It puts the caller on
// the request context and answers 401 when there is none; each route then
// checks its @requirePermission list against the caller's roles.
func (a Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if token, ok := session.BearerToken(r.Header); ok {
			if principal, roles, err := a.resolve(ctx, token); err == nil {
				ctx = session.ContextWithPrincipalID(ctx, principal.ID)
				ctx = session.ContextWithPrincipalName(ctx, principal.Name)
				ctx = session.ContextWithRoles(ctx, roles)
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

// SessionToken is the TokenValidator the shop runs with. Its bearer tokens
// are opaque: a token is its session's jti, a random UUID that only the
// session's holder knows, so a token passes when it is a UUID, and the
// session store then finds its session or refuses it. A shop whose
// identity provider issues JWTs verifies the signature and expiry here
// instead, and returns the jti claim.
func SessionToken(_ context.Context, token string) (string, error) {
	if _, err := scalars.ParseUUID(token); err != nil {
		return "", errors.New("the bearer token is not a session id")
	}
	return token, nil
}

// Roles is the RoleStore the shop runs with. shop-db has no role table, so
// every signed-in user holds one role, whose permissions cover every
// route: products, to read and add them, and orders, to place and read
// them. A shop whose staff alone add products keeps each user's roles in a
// table of its own, or reads its identity provider's groups, and returns
// them here.
type Roles struct{}

func (Roles) ListRolesForPrincipal(context.Context, string) ([]session.Role, error) {
	return []session.Role{{ID: "member", Name: "Member", Permissions: []string{"products", "orders"}}}, nil
}
