// Package shoporders implements the shop-orders API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.
package shoporders

import (
	"context"

	api "example.com/schemas/api/shop-orders"
	types "example.com/schemas/types/go/shop-orders"
)

// New builds the implementation of shop-orders from its dependencies. Its
// signature is the generated one, api.Constructor.
func New(deps api.Deps) (api.Implementations, error) {
	return api.Implementations{
		Order: &Order{deps: deps},
	}, nil
}

var _ api.Constructor = New

// Order implements api.OrderImplementation.
type Order struct {
	deps api.Deps
}

// GetOrder handles GET /api/orders/{id}.
func (impl *Order) GetOrder(ctx context.Context, id string) (*types.OrderView, error) {
	return nil, api.NotImplementedError("Order.GetOrder")
}
