package shoporders

import (
	"context"
	"errors"
	"fmt"
	"time"

	api "example.com/acme/api/shop-orders"
	orm "example.com/acme/orm/shop-db"
	stocksdk "example.com/acme/sdk/go/shop-stock"
	"example.com/acme/shop/acmepay"
	db "example.com/acme/types/go/shop-db"
	engine "github.com/parable-work/superschematic/runtime/engine/go"
	"go.uber.org/zap"
)

// NewJobs builds the jobs of shop-orders from the API's dependencies, as
// New builds its operations. Its signature is the generated one,
// api.JobsConstructor, which the entrypoints the stack's build writes for
// the jobs shop-orders-ship-orders and shop-orders-settle-payments call
// once per run.
func NewJobs(deps api.Deps) (api.Jobs, error) {
	return &Jobs{DB: deps.DB, Engine: deps.Engine, Stock: deps.ShopStock, Pay: payClient(deps), Logger: deps.Logger}, nil
}

var _ api.JobsConstructor = NewJobs

// Jobs implements the jobs of shop-orders.
type Jobs struct {
	DB     orm.DatabaseInterface
	Engine *engine.Background
	Stock  *stocksdk.ShopStockSDK
	Pay    *acmepay.Client
	Logger *zap.Logger
}

// ShipOrders is the warehouse's pick run: it marks every order the worker
// FulfilOrders fulfilled shipped, in one statement, so a run that fails
// part-way ships none and its retry starts over.
func (j *Jobs) ShipOrders(ctx context.Context) error {
	fulfilled := string(db.OrderStatus_Fulfilled)
	shipped := db.OrderStatus_Shipped
	n, err := j.DB.GetOrderRepository().UpdateMany(ctx,
		&orm.OrderFilter{Status: &orm.StringFilter{Eq: &fulfilled}},
		&orm.OrderUpdate{Status: &shipped})
	if err != nil {
		return fmt.Errorf("ship the fulfilled orders: %w", err)
	}
	j.Logger.Info("shipped the fulfilled orders", zap.Int("orders", n))
	return nil
}

// SettlePayments is acmepay's adapter's run (D70). It makes each payment
// action shop-db's engine holds for acmepay and records acmepay's answer;
// it asks acmepay about every held checkout's attempt still processing,
// whose webhook may never have come; and it writes the order of each
// placed checkout that has none, which a crash between the capture and
// the write, or a capture the runner placed after the webhook answered,
// leaves. Each step is idempotent, so a run that stops part-way, and its
// retry, do each thing once.
func (j *Jobs) SettlePayments(ctx context.Context) error {
	made, err := settle(ctx, j.Engine, j.Pay, j.Logger)
	if err != nil {
		return err
	}
	var errs []error
	reconciled, placed := 0, 0
	err = eachCheckout(ctx, j.Engine, "held", func(rec *checkoutRecord) error {
		reconciled++
		return reconcile(ctx, j.Engine, j.Pay, rec)
	})
	errs = append(errs, err)
	// A shop with many placed checkouts would keep a cursor of the last it
	// wrote; this one reads them all, and finds each order by its unique
	// checkoutId.
	err = eachCheckout(ctx, j.Engine, "placed", func(rec *checkoutRecord) error {
		if _, err := placeOrder(ctx, j.DB, j.Stock, j.Logger, time.Now(), rec); err != nil {
			return err
		}
		placed++
		return nil
	})
	errs = append(errs, err)
	j.Logger.Info("settled acmepay's payments", zap.Int("actions", made), zap.Int("held", reconciled), zap.Int("placed", placed))
	return errors.Join(errs...)
}

// eachCheckout calls fn with each checkout in status, a page at a time.
func eachCheckout(ctx context.Context, b *engine.Background, status string, fn func(*checkoutRecord) error) error {
	cursor := ""
	for {
		options := map[string]any{"namespace": namespace, "limit": 100, "where": map[string]any{"Workflow.status": status}}
		if cursor != "" {
			options["cursor"] = cursor
		}
		var page struct {
			Items []checkoutRecord `json:"items"`
			Next  *string          `json:"next"`
		}
		if err := call(ctx, b, &page, "instances.list", adapterPrincipal(), checkoutSchema, options); err != nil {
			return fmt.Errorf("list the %s checkouts: %w", status, err)
		}
		for i := range page.Items {
			if err := fn(&page.Items[i]); err != nil {
				return fmt.Errorf("checkout %s: %w", page.Items[i].ID, err)
			}
		}
		if page.Next == nil || *page.Next == "" {
			return nil
		}
		cursor = *page.Next
	}
}
