package shoporders

import (
	"context"
	"fmt"

	api "example.com/acme/api/shop-orders"
	orm "example.com/acme/orm/shop-db"
	db "example.com/acme/types/go/shop-db"
	"go.uber.org/zap"
)

// NewJobs builds the jobs of shop-orders from the API's dependencies, as
// New builds its operations. Its signature is the generated one,
// api.JobsConstructor, which the entrypoint the stack's build writes for
// the job shop-orders-ship-orders calls once per run.
func NewJobs(deps api.Deps) (api.Jobs, error) {
	return &Jobs{DB: deps.DB, Logger: deps.Logger}, nil
}

var _ api.JobsConstructor = NewJobs

// Jobs implements the jobs of shop-orders.
type Jobs struct {
	DB     orm.DatabaseInterface
	Logger *zap.Logger
}

// ShipOrders is the warehouse's pick run: it marks every placed order
// shipped, in one statement, so a run that fails part-way ships none and
// its retry starts over.
func (j *Jobs) ShipOrders(ctx context.Context) error {
	placed := string(db.OrderStatus_Placed)
	shipped := db.OrderStatus_Shipped
	n, err := j.DB.GetOrderRepository().UpdateMany(ctx,
		&orm.OrderFilter{Status: &orm.StringFilter{Eq: &placed}},
		&orm.OrderUpdate{Status: &shipped})
	if err != nil {
		return fmt.Errorf("ship the placed orders: %w", err)
	}
	j.Logger.Info("shipped the placed orders", zap.Int("orders", n))
	return nil
}
