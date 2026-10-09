// Package depsorders implements the deps-orders API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it. Each job fails
// until you implement it. Each worker fails
// every message until you implement it.
package depsorders

import (
	"context"

	api "example.com/schemas/api/deps-orders"
	depsdb "example.com/schemas/types/go/deps-db"
	types "example.com/schemas/types/go/deps-orders"
)

// New builds the implementation of deps-orders from its dependencies. Its
// signature is the generated one, api.Constructor.
func New(deps api.Deps) (api.Implementations, error) {
	return api.Implementations{
		Order: &Order{deps: deps},
	}, nil
}

var _ api.Constructor = New

// NewJobs builds the jobs of deps-orders from the API's dependencies. Its
// signature is the generated one, api.JobsConstructor.
func NewJobs(deps api.Deps) (api.Jobs, error) {
	return &jobs{deps: deps}, nil
}

var _ api.JobsConstructor = NewJobs

// jobs implements api.Jobs.
type jobs struct {
	deps api.Deps
}

// ReindexOrders runs the job ReindexOrders.
func (j *jobs) ReindexOrders(ctx context.Context) error {
	return api.NotImplementedError("job ReindexOrders")
}

// ShipOrders runs the job ShipOrders.
func (j *jobs) ShipOrders(ctx context.Context) error {
	return api.NotImplementedError("job ShipOrders")
}

// NewWorkers builds the workers of deps-orders from the API's dependencies.
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
func (w *workers) FulfilOrders(ctx context.Context, msg depsdb.OrderPlaced) error {
	return api.NotImplementedError("worker FulfilOrders")
}

// Order implements api.OrderImplementation.
type Order struct {
	deps api.Deps
}

// GetOrder handles GET /api/orders/:id.
func (impl *Order) GetOrder(ctx context.Context, id string) (*types.OrderView, error) {
	return nil, api.NotImplementedError("Order.GetOrder")
}
