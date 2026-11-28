package shoporders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	api "example.com/acme/api/shop-orders"
	orm "example.com/acme/orm/shop-db"
	stocksdk "example.com/acme/sdk/go/shop-stock"
	"example.com/acme/shop/acmepay"
	db "example.com/acme/types/go/shop-db"
	types "example.com/acme/types/go/shop-orders"
	stocktypes "example.com/acme/types/go/shop-stock"
	"github.com/jackc/pgx/v5/pgconn"
	"go.uber.org/zap"
)

// Checkouts implements the checkouts namespace of shop-orders over
// shop-db's Checkout and Refund tables, whose behaviors the repositories
// run through the behaviors' core (D70, D103): a shopper starts the
// checkout shop-cart filled, which holds its units and pickup slot, pays
// for it at acmepay, reads it, and asks for a refund once it is placed. A
// checkout is placed once acmepay's capture is recorded and Payments reads
// it paid; shop-orders then writes its Order row, once, with the order's
// lines at the prices the checkout held them at, and tells shop-stock what
// it sold.
type Checkouts struct {
	DB     orm.DatabaseInterface
	Stock  *stocksdk.ShopStockSDK
	Pay    *acmepay.Client
	Logger *zap.Logger
	Now    func() time.Time
}

var _ api.CheckoutsImplementation = (*Checkouts)(nil)

// callersCheckout reads the checkout with id when the caller may see it:
// their own, or any for staff. Another customer's is the 404 of one no one
// started, as callersOrder answers for an order.
func (c *Checkouts) callersCheckout(ctx context.Context, id types.IdentityUUID) (*db.Checkout, error) {
	customer, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	checkout, err := c.DB.GetCheckoutRepository().GetOne(ctx, id, nil)
	if errors.Is(err, orm.ErrNotFound) {
		return nil, api.NotFoundError("checkout", err)
	}
	if err != nil {
		return nil, err
	}
	if !seesEveryOrder(ctx) && (checkout.Customer.Id == nil || *checkout.Customer.Id != customer) {
		return nil, api.NotFoundError("checkout", orm.ErrNotFound)
	}
	return checkout, nil
}

// StartCheckout records where the order ships and moves the checkout from
// cart to held, which holds each line's units and the pickup slot, all of
// them or none, in one transaction: what does not fit is 409 unavailable,
// its details naming each item and what is free, and the address is not
// kept. A checkout already held answers as it is, so a retried request
// holds nothing twice.
func (c *Checkouts) StartCheckout(ctx context.Context, id types.IdentityUUID, input *types.StartCheckoutInput) (*types.CheckoutView, error) {
	checkout, err := c.callersCheckout(ctx, id)
	if err != nil {
		return nil, err
	}
	switch checkout.Status {
	case string(orm.CheckoutStatusStateHeld):
		return c.view(ctx, checkout)
	case string(orm.CheckoutStatusStateCart):
	default:
		return nil, api.ConflictError(fmt.Sprintf("checkout %s is %s", id, checkout.Status), nil).WithCode("not_in_cart")
	}
	if len(checkout.Lines) == 0 {
		return nil, api.ConflictError("the cart is empty", nil).WithCode("empty_cart")
	}
	shipping := input.ShippingAddress
	address := db.CheckoutAddress{
		Recipient: shipping.Recipient,
		Line1:     shipping.Line1,
		Line2:     shipping.Line2,
		City:      shipping.City,
		Postcode:  shipping.Postcode,
		Country:   shipping.Country,
	}
	err = c.DB.Transaction(ctx, func(tx orm.TxInterface) error {
		checkouts := tx.GetCheckoutRepository()
		if _, err := checkouts.UpdateOne(ctx, id, &orm.CheckoutUpdate{Address: &address}); err != nil {
			return err
		}
		from := orm.CheckoutStatusStateCart
		_, err := checkouts.Status().Transition(ctx, id, orm.CheckoutStatusTransitionParams{From: &from, To: orm.CheckoutStatusStateHeld})
		return err
	})
	if err != nil {
		return nil, refusal(err)
	}
	return c.reread(ctx, id)
}

// PayCheckout opens the checkout's payment and makes its intent at
// acmepay, which the shopper's page pays with the client secret on
// acmepay's own fields. The attempt Payments opens is the intent's
// idempotency key, so a retried request, or SettlePayments making the same
// action, gets the same intent. A payment declined is paid again with a
// new attempt, which voids the one that failed.
func (c *Checkouts) PayCheckout(ctx context.Context, id types.IdentityUUID) (*types.CheckoutPayment, error) {
	checkout, err := c.callersCheckout(ctx, id)
	if err != nil {
		return nil, err
	}
	if checkout.Status != string(orm.CheckoutStatusStateHeld) {
		return nil, api.ConflictError(fmt.Sprintf("checkout %s is %s, and only a held checkout is paid for", id, checkout.Status), nil).WithCode("not_held")
	}
	opened, err := openPayment(ctx, c.DB, id)
	if err != nil {
		return nil, err
	}
	reference := id.ToUUID().String()
	intent, err := c.Pay.CreateIntent(ctx, opened.Attempt, acmepay.IntentRequest{Amount: opened.Amount, Currency: opened.Currency, Reference: reference, Attempt: opened.Attempt})
	if err != nil {
		c.Logger.Warn("acmepay did not make the intent; SettlePayments makes it", zap.String("checkout", reference), zap.Error(err))
		return nil, unavailable("acmepay did not answer: try again")
	}
	// The intent's creation, which acmepay's webhook brings too: recorded
	// under acmepay's event id, it applies once either way.
	if _, err := recordEvent(ctx, c.DB, id, intentEvent(acmepay.EventIntentCreated, intent)); err != nil {
		return nil, refusal(err)
	}
	return &types.CheckoutPayment{
		AttemptId:    opened.Attempt,
		IntentId:     intent.ID,
		ClientSecret: intent.ClientSecret,
		AmountCents:  types.GenericInt64(opened.Amount),
		Currency:     opened.Currency,
	}, nil
}

// openPayment opens the checkout's payment at acmepay: one idempotency key
// per try, so the same try reopens the same attempt and a try whose
// attempt ended without the money, declined or cancelled, moves on to the
// next.
func openPayment(ctx context.Context, database orm.DatabaseInterface, id db.IdentityUUID) (*orm.CheckoutPaymentsOpenPaymentResult, error) {
	payments := database.GetCheckoutRepository().Payments()
	for try := 0; try < maxTries; try++ {
		opened, err := payments.OpenPayment(ctx, id, orm.CheckoutPaymentsOpenPaymentParams{Provider: provider, IdempotencyKey: fmt.Sprintf("%s.%d", id, try)})
		if err != nil {
			return nil, refusal(err)
		}
		if opened.Status != "failed" && opened.Status != "cancelled" {
			return opened, nil
		}
	}
	return nil, api.ConflictError(fmt.Sprintf("checkout %s was paid for %d times without the money", id, maxTries), nil).WithCode("too_many_attempts")
}

// maxTries is how many payments one checkout opens.
const maxTries = 20

// GetCheckout reads the caller's checkout. A placed one's order is
// written now if SettlePayments has not written it yet.
func (c *Checkouts) GetCheckout(ctx context.Context, id types.IdentityUUID) (*types.CheckoutView, error) {
	checkout, err := c.callersCheckout(ctx, id)
	if err != nil {
		return nil, err
	}
	return c.view(ctx, checkout)
}

// RequestRefund asks for a refund of a placed checkout: a Refund row of
// it, which two members of staff sign off, through shop-api's RefundActions
// routes, before it is issued, and SettlePayments then makes at acmepay.
// The repositories stamp the caller as the one who asked for it, whose
// own sign-off Approvals counts for nothing. More than the checkout has
// captured and not refunded is 409 exceeds_refundable.
func (c *Checkouts) RequestRefund(ctx context.Context, id types.IdentityUUID, input *types.RequestRefundInput) (*types.RefundView, error) {
	checkout, err := c.callersCheckout(ctx, id)
	if err != nil {
		return nil, err
	}
	if checkout.Status != string(orm.CheckoutStatusStatePlaced) {
		return nil, api.ConflictError(fmt.Sprintf("checkout %s is %s, and only a placed checkout is refunded", id, checkout.Status), nil).WithCode("not_placed")
	}
	if refundable := count(checkout.PaymentsCaptured) - count(checkout.PaymentsRefunded); int64(input.AmountCents) > refundable {
		return nil, api.ConflictError(fmt.Sprintf("checkout %s has %d to refund", id, refundable), nil).WithCode("exceeds_refundable")
	}
	refund, err := c.DB.GetRefundRepository().CreateOne(ctx, &orm.RefundCreate{
		Checkout:    db.Checkout{Id: &id},
		AmountCents: db.GenericInt64(input.AmountCents),
		Reason:      input.Reason,
	})
	if err != nil {
		return nil, refusal(err)
	}
	view := refundView(refund)
	return &view, nil
}

func refundView(r *db.Refund) types.RefundView {
	view := types.RefundView{Number: r.Number, Status: r.Status, AmountCents: types.GenericInt64(r.AmountCents), Reason: r.Reason}
	if r.Id != nil {
		view.Id = *r.Id
	}
	return view
}

// count is a count a behavior owns, none when it holds none yet.
func count(n *db.GenericInt64) int64 {
	if n == nil {
		return 0
	}
	return int64(*n)
}

func (c *Checkouts) reread(ctx context.Context, id db.IdentityUUID) (*types.CheckoutView, error) {
	checkout, err := c.DB.GetCheckoutRepository().GetOne(ctx, id, nil)
	if errors.Is(err, orm.ErrNotFound) {
		return nil, api.NotFoundError("checkout", err)
	}
	if err != nil {
		return nil, err
	}
	return c.view(ctx, checkout)
}

// heldLines are the checkout's lines as its Holds holds them: each line's
// units of an Inventory, its state, and the price it was held at, from
// Holds' listHolds, the pickup slot left out.
func heldLines(ctx context.Context, database orm.DatabaseInterface, id db.IdentityUUID) ([]orm.CheckoutHoldsListHoldsResultItemsItem, error) {
	held, err := database.GetCheckoutRepository().Holds().ListHolds(ctx, id, json.RawMessage(`{}`))
	if err != nil {
		return nil, refusal(err)
	}
	lines := make([]orm.CheckoutHoldsListHoldsResultItemsItem, 0, len(held.Items))
	for _, item := range held.Items {
		if item.Hold == orm.CheckoutHoldsHoldNameLines {
			lines = append(lines, item)
		}
	}
	return lines, nil
}

// view is the checkout as its shopper sees it, with the order a placed one
// was written as.
func (c *Checkouts) view(ctx context.Context, checkout *db.Checkout) (*types.CheckoutView, error) {
	id := *checkout.Id
	lines, err := heldLines(ctx, c.DB, id)
	if err != nil {
		return nil, err
	}
	view := &types.CheckoutView{
		Id:       id,
		Number:   checkout.Number,
		Status:   types.CheckoutStatus(checkout.Status),
		Lines:    make([]types.CheckoutLineView, 0, len(lines)),
		PickupAt: checkout.PickupAt,
		DueCents: types.GenericInt64(count(checkout.PaymentsDue)),
		Currency: checkout.PaymentsCurrency,
		Payment:  checkout.PaymentsState,
	}
	for _, item := range lines {
		product, err := db.ParseIdentityUUID(item.Id)
		if err != nil {
			continue
		}
		line := types.CheckoutLineView{ProductId: product, State: item.State}
		if item.Quantity != nil {
			line.Quantity = types.GenericInt64(*item.Quantity)
		}
		if item.UnitPrice != nil {
			price := types.GenericInt64(*item.UnitPrice)
			line.UnitPriceCents = &price
		}
		view.Lines = append(view.Lines, line)
	}
	if checkout.Status == string(orm.CheckoutStatusStatePlaced) {
		order, err := placeOrder(ctx, c.DB, c.Stock, c.Logger, c.now(), checkout)
		if err != nil {
			return nil, err
		}
		view.OrderId = order.Id
	}
	return view, nil
}

func (c *Checkouts) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// placeOrder writes the Order row of a placed checkout, once: its
// customer, address and total, a line per line the checkout held at the
// price it held it at, and OrderPlaced, which FulfilOrders handles, in
// one transaction. The unique checkoutId keeps it to one however many
// writers race: the loser reads the winner's. A checkout that wrote its
// order tells shop-stock what it sold, which files a restock request for
// a product it ran low; should that call fail, OrderRestocks' sweep of
// the low stock finds the product.
func placeOrder(ctx context.Context, database orm.DatabaseInterface, stock *stocksdk.ShopStockSDK, logger *zap.Logger, now time.Time, checkout *db.Checkout) (*db.Order, error) {
	checkoutID := *checkout.Id
	orders := database.GetOrderRepository()
	byCheckout := &orm.OrderFilter{CheckoutId: &orm.UUIDFilter{Eq: &checkoutID}}
	if existing, err := orders.FindOne(ctx, byCheckout, nil); err == nil {
		return existing, nil
	} else if !errors.Is(err, orm.ErrNotFound) {
		return nil, err
	}
	var address db.ShippingAddress
	if checkout.Address != nil {
		a := checkout.Address
		address = db.ShippingAddress{Recipient: a.Recipient, Line1: a.Line1, Line2: a.Line2, City: a.City, Postcode: a.Postcode, Country: a.Country}
	}
	held, err := heldLines(ctx, database, checkoutID)
	if err != nil {
		return nil, err
	}
	type line struct {
		product  db.IdentityUUID
		quantity int64
		price    int64
	}
	var lines []line
	for _, item := range held {
		product, err := db.ParseIdentityUUID(item.Id)
		if err != nil {
			return nil, fmt.Errorf("checkout %s's line %d names %q: %w", checkoutID, item.Item, item.Id, err)
		}
		l := line{product: product}
		if item.Quantity != nil {
			l.quantity = *item.Quantity
		}
		if item.UnitPrice != nil {
			l.price = *item.UnitPrice
		}
		lines = append(lines, l)
	}
	var placed *db.Order
	err = database.Transaction(ctx, func(tx orm.TxInterface) error {
		order, err := tx.GetOrderRepository().CreateOne(ctx, &db.Order{
			Customer:        checkout.Customer,
			PlacedAt:        db.TemporalDateTime(now),
			ShippingAddress: address,
			TotalCents:      db.GenericInt64(count(checkout.PaymentsDue)),
			CheckoutId:      &checkoutID,
		})
		if err != nil {
			return err
		}
		for _, l := range lines {
			created, err := tx.GetOrderLineRepository().CreateOne(ctx, &db.OrderLine{
				Order:          db.Order{Id: order.Id},
				Product:        db.Product{Id: &l.product},
				Quantity:       db.GenericInt64(l.quantity),
				UnitPriceCents: db.GenericInt64(l.price),
			})
			if err != nil {
				return err
			}
			order.Lines = append(order.Lines, *created)
		}
		placed = order
		return enqueueOrderPlaced(ctx, tx, &db.OrderPlaced{OrderId: *order.Id})
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return orders.FindOne(ctx, byCheckout, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("write checkout %s's order: %w", checkoutID, err)
	}
	logger.Info("placed the checkout's order", zap.String("checkout", checkoutID.ToUUID().String()), zap.String("order", placed.Id.ToUUID().String()))
	if stock != nil && len(lines) > 0 {
		sold := stocktypes.StockSoldInput{Lines: make([]stocktypes.SoldLine, 0, len(lines))}
		for _, l := range lines {
			sold.Lines = append(sold.Lines, stocktypes.SoldLine{ProductId: l.product, Quantity: stocktypes.GenericInt64(l.quantity)})
		}
		if _, err := stock.StockSalesNamespace.StockSold(ctx, sold); err != nil {
			logger.Warn("shop-stock did not take the sale; OrderRestocks finds what ran low", zap.String("checkout", checkoutID.ToUUID().String()), zap.Error(err))
		}
	}
	return placed, nil
}

// enqueueOrderPlaced writes msg into shop-db's OrderPlaced queue in tx, on
// the database's dialect: the queue's SQLite client on a SQLite file (D116),
// its Postgres one otherwise.
func enqueueOrderPlaced(ctx context.Context, tx orm.TxInterface, msg *db.OrderPlaced) error {
	if lite, ok := tx.(*orm.SQLiteTx); ok {
		return orm.EnqueueOrderPlacedSQLite(ctx, lite.SQL(), msg)
	}
	return orm.EnqueueOrderPlaced(ctx, tx, msg)
}
