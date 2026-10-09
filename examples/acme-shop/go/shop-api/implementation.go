// Package shopapi implements the shop-api API over the generated shop-db
// ORM. It sits where the naming file's [implementation_paths] go template
// puts it, go/{service}, so the entrypoint the stack's build writes for
// shop-api's server imports it and calls New with the server's Deps.
//
// It implements the products routes alone. The user model's routes, sign-in
// and the administration of users and roles, are the identity runtime's,
// and so is authenticating every caller (D50).
package shopapi

import (
	api "example.com/acme/api/shop-api"
)

// New builds the implementation of shop-api from its dependencies. Its
// signature is the generated one, api.Constructor. Products keep their
// images in shop-media, the bucket shop-api lists (D54).
func New(deps api.Deps) (api.Implementations, error) {
	return api.Implementations{
		Product: &Products{DB: deps.DB, Media: deps.ShopMedia},
	}, nil
}

var _ api.Constructor = New
