// Package shoporders implements the shop-orders API over the generated
// shop-db ORM. It sits where the naming file's [implementation_paths] go
// template puts it, go/{service}, so the entrypoint the stack's build
// writes for shop-orders' server imports it and calls New and
// AuthMiddleware with the server's Deps.
package shoporders

import (
	"net/http"

	api "example.com/acme/api/shop-orders"
	"example.com/acme/shop"
)

// New builds the implementation of shop-orders from its dependencies. Its
// signature is the generated one, api.Constructor.
func New(deps api.Deps) (api.Implementations, error) {
	return api.Implementations{
		Order:          &Orders{DB: deps.DB},
		ProductReviews: &Reviews{DB: deps.DB},
	}, nil
}

var _ api.Constructor = New

// AuthMiddleware is the generated Config.AuthMiddleware of shop-orders:
// the shop's Auth over the session and principal stores the session
// provider generates from shop-db's Session and User tables, which then
// tells the ORM who is acting.
func AuthMiddleware(deps api.Deps) (func(http.Handler) http.Handler, error) {
	return Authenticate(shop.Auth{
		Validate:   shop.SessionToken,
		Sessions:   api.NewSessionStore(deps.DB),
		Principals: api.NewPrincipalStore(deps.DB),
		Roles:      shop.Roles{},
	}), nil
}

// Authenticate is shop-orders' auth middleware over auth: auth puts the
// caller on the request context, then actingUser hands the caller to the
// ORM.
func Authenticate(auth shop.Auth) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return auth.Middleware(actingUser(next))
	}
}
