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
	scalars "github.com/parable-work/superscalar/go"
	engine "github.com/parable-work/superschematic/runtime/engine/go"
	"go.uber.org/zap"
)

// namespace is the engine namespace shop-db's engine types live in: the
// database's name, as its outputs.engine leaves it.
const namespace = "shop-db"

// inNamespace is the options every call names the namespace in.
var inNamespace = map[string]any{"namespace": namespace}

// Checkouts implements the checkouts namespace of shop-orders over
// shop-db's engine (D70): a shopper starts the checkout shop-cart filled,
// which holds its units and pickup slot, pays for it at acmepay, reads it,
// and asks for a refund once it is placed. A checkout is placed by the
// Reactions rule on its Payments.state once acmepay's capture is recorded;
// shop-orders then writes its Order row, once, with the order's lines at
// the prices the checkout held them at, and tells shop-stock what it sold.
type Checkouts struct {
	Engine *engine.Background
	DB     orm.DatabaseInterface
	Stock  *stocksdk.ShopStockSDK
	Pay    *acmepay.Client
	Logger *zap.Logger
	Now    func() time.Time
}

var _ api.CheckoutsImplementation = (*Checkouts)(nil)

// checkoutRecord is a Checkout as the engine reads it: its own fields and
// its behaviors'.
type checkoutRecord struct {
	ID   string `json:"id"`
	Data struct {
		CustomerID string          `json:"customerId"`
		Lines      []checkoutLine  `json:"lines"`
		PickupAt   *string         `json:"pickupAt"`
		Address    json.RawMessage `json:"address"`
	} `json:"data"`
	Behaviors struct {
		Workflow struct {
			Status string `json:"status"`
		} `json:"Workflow"`
		Numbering struct {
			Label string `json:"label"`
		} `json:"Numbering"`
		Holds struct {
			Items []holdItem `json:"items"`
			State string     `json:"state"`
		} `json:"Holds"`
		Payments paymentsRecord `json:"Payments"`
	} `json:"behaviors"`
}

type checkoutLine struct {
	InventoryID string `json:"inventoryId"`
	Quantity    int64  `json:"quantity"`
}

// holdItem is one of Holds.items: a line's units of an Inventory, or the
// pickup slot's time on a CollectionPoint.
type holdItem struct {
	Hold      string  `json:"hold"`
	Item      int     `json:"item"`
	Schema    string  `json:"schema"`
	ID        string  `json:"id"`
	Quantity  int64   `json:"quantity"`
	State     string  `json:"state"`
	UnitPrice *int64  `json:"unitPrice"`
	Currency  *string `json:"currency"`
}

// paymentsRecord is Payments' fields.
type paymentsRecord struct {
	Due         int64     `json:"due"`
	Captured    int64     `json:"captured"`
	Refunded    int64     `json:"refunded"`
	Outstanding int64     `json:"outstanding"`
	Currency    *string   `json:"currency"`
	State       string    `json:"state"`
	Attempts    []attempt `json:"attempts"`
}

type attempt struct {
	Attempt     string `json:"attempt"`
	Provider    string `json:"provider"`
	ProviderRef string `json:"providerRef"`
	Amount      int64  `json:"amount"`
	Currency    string `json:"currency"`
	Status      string `json:"status"`
}

// readCheckout reads a checkout as principal: nil when there is none.
func readCheckout(ctx context.Context, b *engine.Background, principal *engine.Principal, id string) (*checkoutRecord, error) {
	var rec *checkoutRecord
	if err := call(ctx, b, &rec, "instances.get", principal, checkoutSchema, id, inNamespace); err != nil {
		return nil, err
	}
	return rec, nil
}

// callersCheckout reads the checkout with id when the caller may see it:
// their own, or any for staff. Another customer's is the 404 of one no one
// started, as callersOrder answers for an order.
func (c *Checkouts) callersCheckout(ctx context.Context, id types.IdentityUUID) (*checkoutRecord, *engine.Principal, error) {
	customer, err := caller(ctx)
	if err != nil {
		return nil, nil, err
	}
	// An engine id is a UUID's canonical text, as Identity.UUID writes it,
	// which is how the engine keeps the checkout's customerId too.
	principal := shopperPrincipal(customer.String())
	rec, err := readCheckout(ctx, c.Engine, principal, id.String())
	if err != nil {
		return nil, nil, err
	}
	if rec == nil || (!seesEveryOrder(ctx) && rec.Data.CustomerID != customer.String()) {
		return nil, nil, api.NotFoundError("checkout", nil)
	}
	return rec, principal, nil
}

// StartCheckout records where the order ships and moves the checkout from
// cart to held, which holds each line's units and the pickup slot, all of
// them or none: what does not fit is 409 unavailable, its details naming
// each item and what is free. A checkout already held answers as it is,
// so a retried request holds nothing twice.
func (c *Checkouts) StartCheckout(ctx context.Context, id types.IdentityUUID, input *types.StartCheckoutInput) (*types.CheckoutView, error) {
	rec, principal, err := c.callersCheckout(ctx, id)
	if err != nil {
		return nil, err
	}
	switch rec.Behaviors.Workflow.Status {
	case "held":
		return c.view(ctx, rec)
	case "cart":
	default:
		return nil, api.ConflictError(fmt.Sprintf("checkout %s is %s", id, rec.Behaviors.Workflow.Status), nil).WithCode("not_in_cart")
	}
	if len(rec.Data.Lines) == 0 {
		return nil, api.ConflictError("the cart is empty", nil).WithCode("empty_cart")
	}
	address := map[string]any{
		"recipient": input.ShippingAddress.Recipient,
		"line1":     input.ShippingAddress.Line1,
		"city":      input.ShippingAddress.City,
		"postcode":  input.ShippingAddress.Postcode,
		"country":   input.ShippingAddress.Country,
	}
	if input.ShippingAddress.Line2 != "" {
		address["line2"] = input.ShippingAddress.Line2
	}
	if err := call(ctx, c.Engine, nil, "instances.update", principal, checkoutSchema, rec.ID, map[string]any{"address": address}, inNamespace); err != nil {
		return nil, err
	}
	if err := call(ctx, c.Engine, nil, "instances.invoke", principal, checkoutSchema, rec.ID, "transition", map[string]any{"to": "held"}, inNamespace); err != nil {
		return nil, err
	}
	return c.reread(ctx, principal, rec.ID)
}

// PayCheckout opens the checkout's payment and makes its intent at
// acmepay, which the shopper's page pays with the client secret on
// acmepay's own fields. The engine's attempt is the intent's idempotency
// key, so a retried request, or SettlePayments making the same action,
// gets the same intent. A payment declined is paid again with a new
// attempt, which voids the one that failed.
func (c *Checkouts) PayCheckout(ctx context.Context, id types.IdentityUUID) (*types.CheckoutPayment, error) {
	rec, principal, err := c.callersCheckout(ctx, id)
	if err != nil {
		return nil, err
	}
	if rec.Behaviors.Workflow.Status != "held" {
		return nil, api.ConflictError(fmt.Sprintf("checkout %s is %s, and only a held checkout is paid for", id, rec.Behaviors.Workflow.Status), nil).WithCode("not_held")
	}
	// One key per try: the attempts that ended without the money count
	// the tries before, so a retry of this one reopens the same attempt.
	tries := 0
	for _, a := range rec.Behaviors.Payments.Attempts {
		if a.Status == "failed" || a.Status == "cancelled" {
			tries++
		}
	}
	var opened struct {
		Attempt  string `json:"attempt"`
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
	}
	key := fmt.Sprintf("%s.%d", rec.ID, tries)
	if err := call(ctx, c.Engine, &opened, "instances.invoke", principal, checkoutSchema, rec.ID, "openPayment", map[string]any{"provider": provider, "idempotencyKey": key}, inNamespace); err != nil {
		return nil, err
	}
	intent, err := c.Pay.CreateIntent(ctx, opened.Attempt, acmepay.IntentRequest{Amount: opened.Amount, Currency: opened.Currency, Reference: rec.ID, Attempt: opened.Attempt})
	if err != nil {
		c.Logger.Warn("acmepay did not make the intent; SettlePayments makes it", zap.String("checkout", rec.ID), zap.Error(err))
		return nil, unavailable("acmepay did not answer: try again")
	}
	// The intent's creation, which acmepay's webhook brings too: recorded
	// under acmepay's event id, it applies once either way.
	if err := recordEvent(ctx, c.Engine, rec.ID, intentEvent(acmepay.EventIntentCreated, intent)); err != nil {
		return nil, err
	}
	return &types.CheckoutPayment{
		AttemptId:    opened.Attempt,
		IntentId:     intent.ID,
		ClientSecret: intent.ClientSecret,
		AmountCents:  types.GenericInt64(opened.Amount),
		Currency:     opened.Currency,
	}, nil
}

// GetCheckout reads the caller's checkout. A placed one's order is
// written now if SettlePayments has not written it yet.
func (c *Checkouts) GetCheckout(ctx context.Context, id types.IdentityUUID) (*types.CheckoutView, error) {
	rec, _, err := c.callersCheckout(ctx, id)
	if err != nil {
		return nil, err
	}
	return c.view(ctx, rec)
}

// RequestRefund asks for a refund of a placed checkout: a Refund linked to
// it, which two members of staff sign off at /engine/shop-db before it is
// issued, and SettlePayments then makes at acmepay. More than the checkout
// has captured and not refunded is 409 exceeds_refundable.
func (c *Checkouts) RequestRefund(ctx context.Context, id types.IdentityUUID, input *types.RequestRefundInput) (*types.RefundView, error) {
	rec, principal, err := c.callersCheckout(ctx, id)
	if err != nil {
		return nil, err
	}
	if rec.Behaviors.Workflow.Status != "placed" {
		return nil, api.ConflictError(fmt.Sprintf("checkout %s is %s, and only a placed checkout is refunded", id, rec.Behaviors.Workflow.Status), nil).WithCode("not_placed")
	}
	payments := rec.Behaviors.Payments
	if refundable := payments.Captured - payments.Refunded; int64(input.AmountCents) > refundable {
		return nil, api.ConflictError(fmt.Sprintf("checkout %s has %d to refund", id, refundable), nil).WithCode("exceeds_refundable")
	}
	var refund refundRecord
	data := map[string]any{"amountCents": int64(input.AmountCents), "reason": input.Reason, "requestedBy": principal.Subject}
	// Its id is a UUID's canonical text, as the API answers it.
	options := map[string]any{"namespace": namespace, "id": scalars.NewUUID().String(), "behaviors": map[string]any{"Links": map[string]any{"order": rec.ID}}}
	if err := call(ctx, c.Engine, &refund, "instances.create", principal, refundSchema, data, options); err != nil {
		return nil, err
	}
	view := refund.view()
	return &view, nil
}

// refundRecord is a Refund as the engine reads it.
type refundRecord struct {
	ID   string `json:"id"`
	Data struct {
		AmountCents int64  `json:"amountCents"`
		Reason      string `json:"reason"`
	} `json:"data"`
	Behaviors struct {
		Workflow struct {
			Status string `json:"status"`
		} `json:"Workflow"`
		Numbering struct {
			Label string `json:"label"`
		} `json:"Numbering"`
	} `json:"behaviors"`
}

func (r refundRecord) view() types.RefundView {
	id, _ := scalars.ParseUUID(r.ID)
	return types.RefundView{
		Id:          id,
		Number:      r.Behaviors.Numbering.Label,
		Status:      r.Behaviors.Workflow.Status,
		AmountCents: types.GenericInt64(r.Data.AmountCents),
		Reason:      r.Data.Reason,
	}
}

func (c *Checkouts) reread(ctx context.Context, principal *engine.Principal, id string) (*types.CheckoutView, error) {
	rec, err := readCheckout(ctx, c.Engine, principal, id)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, api.NotFoundError("checkout", nil)
	}
	return c.view(ctx, rec)
}

// view is the checkout as its shopper sees it, with the order a placed one
// was written as.
func (c *Checkouts) view(ctx context.Context, rec *checkoutRecord) (*types.CheckoutView, error) {
	id, err := scalars.ParseUUID(rec.ID)
	if err != nil {
		return nil, fmt.Errorf("checkout id %q: %w", rec.ID, err)
	}
	view := &types.CheckoutView{
		Id:       id,
		Number:   rec.Behaviors.Numbering.Label,
		Status:   types.CheckoutStatus(rec.Behaviors.Workflow.Status),
		Lines:    []types.CheckoutLineView{},
		DueCents: types.GenericInt64(rec.Behaviors.Payments.Due),
		Payment:  rec.Behaviors.Payments.State,
	}
	if rec.Behaviors.Payments.Currency != nil {
		view.Currency = *rec.Behaviors.Payments.Currency
	}
	if rec.Data.PickupAt != nil {
		at, err := time.Parse(time.RFC3339Nano, *rec.Data.PickupAt)
		if err == nil {
			pickup := types.TemporalDateTime(at)
			view.PickupAt = &pickup
		}
	}
	for _, item := range rec.Behaviors.Holds.Items {
		if item.Hold != "lines" {
			continue
		}
		product, err := scalars.ParseUUID(item.ID)
		if err != nil {
			continue
		}
		line := types.CheckoutLineView{ProductId: product, Quantity: types.GenericInt64(item.Quantity), State: item.State}
		if item.UnitPrice != nil {
			price := types.GenericInt64(*item.UnitPrice)
			line.UnitPriceCents = &price
		}
		view.Lines = append(view.Lines, line)
	}
	if rec.Behaviors.Workflow.Status == "placed" {
		order, err := placeOrder(ctx, c.DB, c.Stock, c.Logger, c.now(), rec)
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
// Stock.low finds the product.
func placeOrder(ctx context.Context, database orm.DatabaseInterface, stock *stocksdk.ShopStockSDK, logger *zap.Logger, now time.Time, rec *checkoutRecord) (*db.Order, error) {
	checkoutID, err := scalars.ParseUUID(rec.ID)
	if err != nil {
		return nil, fmt.Errorf("checkout id %q: %w", rec.ID, err)
	}
	orders := database.GetOrderRepository()
	byCheckout := &orm.OrderFilter{CheckoutId: &orm.UUIDFilter{Eq: &checkoutID}}
	if existing, err := orders.FindOne(ctx, byCheckout, nil); err == nil {
		return existing, nil
	} else if !errors.Is(err, orm.ErrNotFound) {
		return nil, err
	}
	customer, err := scalars.ParseUUID(rec.Data.CustomerID)
	if err != nil {
		return nil, fmt.Errorf("checkout %s's customer %q: %w", rec.ID, rec.Data.CustomerID, err)
	}
	var address db.ShippingAddress
	if len(rec.Data.Address) > 0 {
		if err := json.Unmarshal(rec.Data.Address, &address); err != nil {
			return nil, fmt.Errorf("checkout %s's address: %w", rec.ID, err)
		}
	}
	type line struct {
		product  db.IdentityUUID
		quantity int64
		price    int64
	}
	var lines []line
	for _, item := range rec.Behaviors.Holds.Items {
		if item.Hold != "lines" {
			continue
		}
		product, err := scalars.ParseUUID(item.ID)
		if err != nil {
			return nil, fmt.Errorf("checkout %s's line %d names %q: %w", rec.ID, item.Item, item.ID, err)
		}
		price := int64(0)
		if item.UnitPrice != nil {
			price = *item.UnitPrice
		}
		lines = append(lines, line{product: product, quantity: item.Quantity, price: price})
	}
	var placed *db.Order
	err = database.Transaction(ctx, func(tx orm.TxInterface) error {
		order, err := tx.GetOrderRepository().CreateOne(ctx, &db.Order{
			Customer:        db.User{Id: &customer},
			PlacedAt:        db.TemporalDateTime(now),
			ShippingAddress: address,
			TotalCents:      db.GenericInt64(rec.Behaviors.Payments.Due),
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
		return orm.EnqueueOrderPlaced(ctx, tx, &db.OrderPlaced{OrderId: *order.Id})
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return orders.FindOne(ctx, byCheckout, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("write checkout %s's order: %w", rec.ID, err)
	}
	logger.Info("placed the checkout's order", zap.String("checkout", rec.ID), zap.String("order", placed.Id.ToUUID().String()))
	if stock != nil && len(lines) > 0 {
		sold := stocktypes.StockSoldInput{Lines: make([]stocktypes.SoldLine, 0, len(lines))}
		for _, l := range lines {
			sold.Lines = append(sold.Lines, stocktypes.SoldLine{ProductId: l.product, Quantity: stocktypes.GenericInt64(l.quantity)})
		}
		if _, err := stock.StockSalesNamespace.StockSold(ctx, sold); err != nil {
			logger.Warn("shop-stock did not take the sale; OrderRestocks finds what ran low", zap.String("checkout", rec.ID), zap.Error(err))
		}
	}
	return placed, nil
}
