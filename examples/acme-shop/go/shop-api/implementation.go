// Package shopapi implements the shop-api API over the generated shop-db
// ORM. It sits where the naming file's [implementation_paths] go template
// puts it, go/{service}, so the entrypoint the stack's build writes for
// shop-api's server imports it and calls New and AuthMiddleware with the
// server's Deps.
package shopapi

import (
	"net/http"

	api "example.com/acme/api/shop-api"
	"example.com/acme/shop"
)

// New builds the implementation of shop-api from its dependencies. Its
// signature is the generated one, api.Constructor.
func New(deps api.Deps) (api.Implementations, error) {
	return api.Implementations{
		Product: &Products{DB: deps.DB},
	}, nil
}

var _ api.Constructor = New

// AuthMiddleware is the generated Config.AuthMiddleware of shop-api: the
// shop's Auth over the session and principal stores the session provider
// generates from shop-db's Session and User tables.
func AuthMiddleware(deps api.Deps) (func(http.Handler) http.Handler, error) {
	auth := shop.Auth{
		Validate:   shop.SessionToken,
		Sessions:   api.NewSessionStore(deps.DB),
		Principals: api.NewPrincipalStore(deps.DB),
		Roles:      shop.Roles{},
	}
	return auth.Middleware, nil
}
