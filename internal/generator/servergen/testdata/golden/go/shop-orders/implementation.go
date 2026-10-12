// Package shoporders implements the shop-orders API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it. Each job fails
// until you implement it. Each worker fails
// every message until you implement it.
package shoporders

import (
	"context"
	"errors"

	runtimemiddleware "github.com/parable-work/superschematic/runtime/http/go/middleware"

	api "example.com/schemas/api/shop-orders"
	shopdb "example.com/schemas/types/go/shop-db"
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

// NewJobs builds the jobs of shop-orders from the API's dependencies. Its
// signature is the generated one, api.JobsConstructor.
func NewJobs(deps api.Deps) (api.Jobs, error) {
	return &jobs{deps: deps}, nil
}

var _ api.JobsConstructor = NewJobs

// jobs implements api.Jobs.
type jobs struct {
	deps api.Deps
}

// ExpireOrders runs the job ExpireOrders.
func (j *jobs) ExpireOrders(ctx context.Context) error {
	return api.NotImplementedError("job ExpireOrders")
}

// NewWorkers builds the workers of shop-orders from the API's dependencies.
// Its signature is the generated one, api.WorkersConstructor.
func NewWorkers(deps api.Deps) (api.Workers, error) {
	return &workers{deps: deps}, nil
}

var _ api.WorkersConstructor = NewWorkers

// workers implements api.Workers.
type workers struct {
	deps api.Deps
}

// FulfilOrders handles a message of the queue OrderPlaced.
func (w *workers) FulfilOrders(ctx context.Context, msg shopdb.OrderPlaced) error {
	return api.NotImplementedError("worker FulfilOrders")
}

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
