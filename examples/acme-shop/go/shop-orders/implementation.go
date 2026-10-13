// Package shoporders implements the shop-orders API over the generated
// shop-db ORM. It sits where the naming file's [implementation_paths] go
// template puts it, go/{service}, so the entrypoint the stack's build
// writes for shop-orders' server imports it and calls New with the
// server's Deps. The identity runtime authenticates its callers, with the
// sessions shop-api's sign-in starts (D50). Placing an order reserves its
// stock on shop-stock, a Rust server, through the client of shop-stock's
// Go SDK its Deps holds (D57).
package shoporders

import (
	api "example.com/acme/api/shop-orders"
)

// New builds the implementation of shop-orders from its dependencies. Its
// signature is the generated one, api.Constructor.
func New(deps api.Deps) (api.Implementations, error) {
	return api.Implementations{
		Order:          &Orders{DB: deps.DB, Stock: deps.ShopStock},
		ProductReviews: &Reviews{DB: deps.DB},
	}, nil
}

var _ api.Constructor = New
