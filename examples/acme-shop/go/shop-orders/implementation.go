// Package shoporders implements the shop-orders API over the generated
// shop-db ORM. It sits where the naming file's [implementation_paths] go
// template puts it, go/{service}, so the entrypoint the stack's build
// writes for shop-orders' server imports it and calls New with the
// server's Deps. The identity runtime authenticates its callers, with the
// sessions shop-api's sign-in starts (D50).
package shoporders

import (
	api "example.com/acme/api/shop-orders"
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
