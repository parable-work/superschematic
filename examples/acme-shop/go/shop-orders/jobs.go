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
	"go.uber.org/zap"
)

// NewJobs builds the jobs of shop-orders from the API's dependencies, as
// New builds its operations. Its signature is the generated one,
// api.JobsConstructor, which the entrypoints the stack's build writes for
// the jobs shop-orders-ship-orders and shop-orders-settle-payments call
// once per run.
func NewJobs(deps api.Deps) (api.Jobs, error) {
	return &Jobs{DB: deps.DB, Stock: deps.ShopStock, Pay: payClient(deps), Logger: deps.Logger}, nil
}

var _ api.JobsConstructor = NewJobs

// Jobs implements the jobs of shop-orders.
type Jobs struct {
	DB     orm.DatabaseInterface
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
// action shop-db's checkouts hold for acmepay and records acmepay's
// answer; it asks acmepay about every held checkout's attempt still
// processing, whose webhook may never have come; it places each held
// checkout that is paid, which a webhook that failed before the move
// leaves, and issues each approved refund acmepay has made; and it writes
// the order of each placed checkout that has none, which a crash between
// the move and the write leaves. Each step is idempotent, so a run that
// stops part-way, and its retry, do each thing once.
func (j *Jobs) SettlePayments(ctx context.Context) error {
	made, err := settle(ctx, j.DB, j.Pay, j.Logger)
	if err != nil {
		return err
	}
	var errs []error
	reconciled, moved, issued, placed := 0, 0, 0, 0
	held := string(orm.CheckoutStatusStateHeld)
	err = eachCheckout(ctx, j.DB, &orm.CheckoutFilter{Status: &orm.StringFilter{Eq: &held}}, func(checkout *db.Checkout) error {
		reconciled++
		return reconcile(ctx, j.DB, j.Pay, checkout)
	})
	errs = append(errs, err)
	paid := "paid"
	err = eachCheckout(ctx, j.DB, &orm.CheckoutFilter{Status: &orm.StringFilter{Eq: &held}, PaymentsState: &orm.StringFilter{Eq: &paid}}, func(checkout *db.Checkout) error {
		ok, err := place(ctx, j.DB, *checkout.Id)
		if ok {
			moved++
		}
		return err
	})
	errs = append(errs, err)
	issued, err = issueRefunds(ctx, j.DB)
	errs = append(errs, err)
	// A shop with many placed checkouts would keep a cursor of the last it
	// wrote; this one reads them all, and finds each order by its unique
	// checkoutId.
	placedState := string(orm.CheckoutStatusStatePlaced)
	err = eachCheckout(ctx, j.DB, &orm.CheckoutFilter{Status: &orm.StringFilter{Eq: &placedState}}, func(checkout *db.Checkout) error {
		if _, err := placeOrder(ctx, j.DB, j.Stock, j.Logger, time.Now(), checkout); err != nil {
			return err
		}
		placed++
		return nil
	})
	errs = append(errs, err)
	j.Logger.Info("settled acmepay's payments",
		zap.Int("actions", made), zap.Int("held", reconciled), zap.Int("moved", moved), zap.Int("refunds", issued), zap.Int("placed", placed))
	return errors.Join(errs...)
}

// issueRefunds moves each approved Refund whose refund acmepay made, and
// the checkout's Payments settled on it, to issued.
func issueRefunds(ctx context.Context, database orm.DatabaseInterface) (int, error) {
	approved := string(orm.RefundStatusStateApproved)
	refunded := "refunded"
	refunds, _, err := database.GetRefundRepository().FindMany(ctx,
		&orm.RefundFilter{Status: &orm.StringFilter{Eq: &approved}, PaymentsState: &orm.StringFilter{Eq: &refunded}}, &orm.RefundFindOptions{Fields: orm.RefundFields{}.All()})
	if err != nil {
		return 0, fmt.Errorf("list the refunds acmepay made: %w", err)
	}
	issued := 0
	for _, refund := range refunds {
		from := orm.RefundStatusStateApproved
		_, err := database.GetRefundRepository().Status().Transition(asAdapter(ctx), *refund.Id, orm.RefundStatusTransitionParams{From: &from, To: orm.RefundStatusStateIssued})
		if v, ok := vetoOf(err); ok && v.VetoCode == "stale" {
			continue
		}
		if err != nil {
			return issued, fmt.Errorf("issue refund %s: %w", refund.Id, err)
		}
		issued++
	}
	return issued, nil
}

// eachCheckout calls fn with each checkout filter finds, a page at a time.
func eachCheckout(ctx context.Context, database orm.DatabaseInterface, filter *orm.CheckoutFilter, fn func(*db.Checkout) error) error {
	opts := &orm.CheckoutPageOptions{Fields: orm.CheckoutFields{}.All(), Limit: 100}
	for {
		page, err := database.GetCheckoutRepository().FindPage(ctx, filter, opts)
		if err != nil {
			return fmt.Errorf("list the checkouts: %w", err)
		}
		for _, checkout := range page.Rows {
			if err := fn(checkout); err != nil {
				return fmt.Errorf("checkout %s: %w", checkout.Id, err)
			}
		}
		if page.Next == nil {
			return nil
		}
		opts.After = page.Next
	}
}
