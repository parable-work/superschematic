// Package shoporders implements the shop-orders API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.
package shoporders

import (
	"context"
	"errors"

	runtimemiddleware "github.com/parable-work/superschematic/runtime/http/go/middleware"

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

// PayloadDecryptor decrypts the request bodies of the encrypted operations
// of shop-orders. The server's entrypoint passes it to the generated
// Config.PayloadDecryptor. The scaffold's decrypts nothing, so each encrypted
// operation is refused until it returns your key service's decryptor.
func PayloadDecryptor(deps api.Deps) (runtimemiddleware.PayloadDecryptor, error) {
	return noDecryptor{}, nil
}

// noDecryptor refuses every payload.
type noDecryptor struct{}

func (noDecryptor) Decrypt(context.Context, string) ([]byte, error) {
	return nil, errors.New("shop-orders has no payload decryptor yet")
}

// Order implements api.OrderImplementation.
type Order struct {
	deps api.Deps
}

// PlaceOrder handles POST /api/orders.
func (impl *Order) PlaceOrder(ctx context.Context, sku string) (*types.OrderView, error) {
	return nil, api.NotImplementedError("Order.PlaceOrder")
}

// GetOrder handles GET /api/orders/{id}.
func (impl *Order) GetOrder(ctx context.Context, id string) (*types.OrderView, error) {
	return nil, api.NotImplementedError("Order.GetOrder")
}
