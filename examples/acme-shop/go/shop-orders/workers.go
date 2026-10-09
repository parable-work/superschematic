package shoporders

import (
	"context"
	"errors"
	"fmt"

	api "example.com/acme/api/shop-orders"
	orm "example.com/acme/orm/shop-db"
	db "example.com/acme/types/go/shop-db"
	"go.uber.org/zap"
)

// NewWorkers builds the workers of shop-orders from the API's
// dependencies, as New builds its operations. Its signature is the
// generated one, api.WorkersConstructor, which the entrypoint the stack's
// build writes for the worker shop-orders-fulfil-orders calls once (D53).
func NewWorkers(deps api.Deps) (api.Workers, error) {
	return &Workers{DB: deps.DB, Logger: deps.Logger}, nil
}

var _ api.WorkersConstructor = NewWorkers

// Workers implements the workers of shop-orders.
type Workers struct {
	DB     orm.DatabaseInterface
	Logger *zap.Logger
}

// FulfilOrders handles one OrderPlaced message, which PlaceOrder enqueues
// with the order: it marks the order fulfilled, for ShipOrders to ship. A
// message may come again, after a worker dies holding it, so an order no
// longer placed, already fulfilled, shipped or cancelled, is left as it
// is, and the message is done.
func (w *Workers) FulfilOrders(ctx context.Context, msg db.OrderPlaced) error {
	placed := string(db.OrderStatus_Placed)
	fulfilled := db.OrderStatus_Fulfilled
	id := msg.OrderId
	n, err := w.DB.GetOrderRepository().UpdateMany(ctx,
		&orm.OrderFilter{Id: &orm.UUIDFilter{Eq: &id}, Status: &orm.StringFilter{Eq: &placed}},
		&orm.OrderUpdate{Status: &fulfilled})
	if err != nil {
		return fmt.Errorf("fulfil order %s: %w", id, err)
	}
	if n == 0 {
		order, err := w.DB.GetOrderRepository().GetOne(ctx, id, nil)
		if errors.Is(err, orm.ErrNotFound) {
			return fmt.Errorf("fulfil order %s: no such order", id)
		}
		if err != nil {
			return fmt.Errorf("fulfil order %s: %w", id, err)
		}
		w.Logger.Info("order was fulfilled already", zap.String("order", id.ToUUID().String()), zap.String("status", string(order.Status)))
		return nil
	}
	w.Logger.Info("fulfilled the order", zap.String("order", id.ToUUID().String()))
	return nil
}
