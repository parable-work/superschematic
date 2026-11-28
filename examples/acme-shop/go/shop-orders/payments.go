package shoporders

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	api "example.com/acme/api/shop-orders"
	orm "example.com/acme/orm/shop-db"
	stocksdk "example.com/acme/sdk/go/shop-stock"
	"example.com/acme/shop/acmepay"
	db "example.com/acme/types/go/shop-db"
	types "example.com/acme/types/go/shop-orders"
	scalars "github.com/parable-work/superscalar/go"
	"go.uber.org/zap"
)

// acmepay's adapter (D70), which is shop-orders' own code: no behavior
// opens a connection to a provider. Checkout's Payments records what is to
// be done, the intents, captures, voids and refunds, as actions;
// payCheckout and the job SettlePayments make them at acmepay, and every
// outcome, from acmepay's answer or its webhook, is recorded with
// recordPaymentEvent as acmepay's event, which applies once per event id.
// Once a capture leaves a held checkout paid, the adapter moves it to
// placed, which Payments allows only then; once a refund leaves an
// approved Refund refunded, it moves the Refund to issued.

// kinds maps acmepay's event types to the kinds recordPaymentEvent takes;
// an event of another type is acknowledged and dropped.
var kinds = map[string]string{
	acmepay.EventIntentCreated:    "requiresAction",
	acmepay.EventIntentProcessing: "processing",
	acmepay.EventIntentSucceeded:  "captured",
	acmepay.EventIntentFailed:     "failed",
	acmepay.EventIntentCanceled:   "cancelled",
	acmepay.EventRefundSucceeded:  "refunded",
	acmepay.EventRefundFailed:     "refundFailed",
}

// eventParams are recordPaymentEvent's parameters for an acmepay event:
// the intent it is about, with Payments' attempt, or the refund, with
// Payments' refund. No payer's name, email or card leaves acmepay, so none
// is recorded.
func eventParams(event acmepay.Event) (orm.CheckoutPaymentsRecordPaymentEventParams, bool) {
	kind, ok := kinds[event.Type]
	if !ok {
		return orm.CheckoutPaymentsRecordPaymentEventParams{}, false
	}
	object := event.Data.Object
	params := orm.CheckoutPaymentsRecordPaymentEventParams{
		Provider: provider,
		EventId:  event.ID,
		Kind:     kind,
		Amount:   object.Amount,
		Currency: object.Currency,
		At:       event.Created * 1000,
	}
	if object.ID != "" {
		params.ProviderRef = &object.ID
	}
	if object.Object == "refund" {
		params.Refund = &object.Refund
	} else if object.Attempt != "" {
		params.Attempt = &object.Attempt
	}
	return params, true
}

// intentEvent is the event acmepay made for an intent's last change, from
// the intent its API answered with.
func intentEvent(eventType string, intent acmepay.Intent) acmepay.Event {
	event := acmepay.Event{ID: intent.Event, Type: eventType, Created: intent.EventCreated}
	event.Data.Object = acmepay.Object{
		ID: intent.ID, Object: "payment_intent", Amount: intent.Amount, Currency: intent.Currency,
		Status: intent.Status, Reference: intent.Reference, Attempt: intent.Attempt,
	}
	return event
}

// recordEvent records an acmepay event on the checkout it is about, as the
// adapter, and answers the checkout's Payments state after it: none for an
// event of a type the shop does not record.
func recordEvent(ctx context.Context, database orm.DatabaseInterface, checkout db.IdentityUUID, event acmepay.Event) (string, error) {
	params, ok := eventParams(event)
	if !ok {
		return "", nil
	}
	recorded, err := database.GetCheckoutRepository().Payments().RecordPaymentEvent(asAdapter(ctx), checkout, params)
	if err != nil {
		return "", err
	}
	return recorded.State, nil
}

// place moves a held checkout that is paid to placed, as the adapter: the
// move commits what it holds, its units sold and its slot booked. One no
// longer held, by an earlier move or an expiry, is left as it is.
func place(ctx context.Context, database orm.DatabaseInterface, checkout db.IdentityUUID) (bool, error) {
	from := orm.CheckoutStatusStateHeld
	_, err := database.GetCheckoutRepository().Status().Transition(asAdapter(ctx), checkout, orm.CheckoutStatusTransitionParams{From: &from, To: orm.CheckoutStatusStatePlaced})
	if v, ok := vetoOf(err); ok && v.VetoCode == "stale" {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// PaymentWebhooks implements acmepay's webhook. Its verifier has checked
// the signature by then (Verifier).
type PaymentWebhooks struct {
	DB     orm.DatabaseInterface
	Stock  *stocksdk.ShopStockSDK
	Logger *zap.Logger
}

var _ api.PaymentWebhooksImplementation = (*PaymentWebhooks)(nil)

// PaymentEvents records an acmepay event on the checkout its reference
// names, and answers quickly: acmepay sends the event again until it hears
// 2xx. An event of a type the shop does not record, one about no
// checkout, and one a behavior vetoes (a currency_mismatch, say), are
// acknowledged, the last logged, so acmepay does not send them forever; a
// checkout whose row kept moving is 503, which acmepay tries again. A
// capture that leaves the checkout paid places it, and writes its order;
// should that fail, SettlePayments places it and writes it.
func (w *PaymentWebhooks) PaymentEvents(ctx context.Context, in *types.AcmepayEvent) (*types.PaymentEventReceipt, error) {
	event := acmepay.Event{ID: in.Id, Type: in.Type, Created: int64(in.Created)}
	object := in.Data.Object
	event.Data.Object = acmepay.Object{
		ID: object.Id, Object: object.Object, Amount: int64(object.Amount), Currency: object.Currency, Status: object.Status,
		PaymentIntent: object.PaymentIntent, Reference: object.Reference, Attempt: object.Attempt, Refund: object.Refund,
	}
	logger := w.Logger.With(zap.String("event", in.Id), zap.String("type", in.Type))
	checkout, err := scalars.ParseUUID(event.Data.Object.Reference)
	if _, ok := kinds[in.Type]; !ok || err != nil {
		logger.Info("dropped an acmepay event the shop does not record")
		return &types.PaymentEventReceipt{Received: true}, nil
	}
	state, err := recordEvent(ctx, w.DB, checkout, event)
	if err != nil {
		err = refusal(err)
		var unavailableErr *unavailableError
		var appErr *api.AppError
		switch {
		case errors.As(err, &unavailableErr):
			return nil, err
		case errors.As(err, &appErr) && appErr.HTTPStatus() < 500:
			logger.Warn("the checkout refused acmepay's event, or there is none", zap.Error(err))
			return &types.PaymentEventReceipt{Received: true}, nil
		}
		return nil, err
	}
	if kinds[in.Type] == "captured" && state == "paid" {
		if _, err := place(ctx, w.DB, checkout); err != nil {
			logger.Warn("the checkout is placed when SettlePayments runs", zap.Error(err))
			return &types.PaymentEventReceipt{Received: true}, nil
		}
		placed, err := w.DB.GetCheckoutRepository().GetOne(ctx, checkout, nil)
		if err == nil && placed.Status == string(orm.CheckoutStatusStatePlaced) {
			if _, err := placeOrder(ctx, w.DB, w.Stock, logger, time.Now(), placed); err != nil {
				logger.Warn("the order is written when SettlePayments runs", zap.Error(err))
			}
		}
	}
	return &types.PaymentEventReceipt{Received: true}, nil
}

// Verifier checks each acmepay webhook's signature before anything else
// runs (D26): the HMAC of its timestamp and its raw body under the signing
// secret, within five minutes of now. A request it refuses is 401, and
// never reaches PaymentEvents.
func Verifier(secret string, now func() time.Time) api.WebhookVerifier {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				api.RespondError(w, r, http.StatusBadRequest, "unreadable webhook body")
				return
			}
			if err := acmepay.Verify(r.Header.Get(acmepay.SignatureHeader), body, now(), acmepay.DefaultTolerance, secret); err != nil {
				api.RespondError(w, r, http.StatusUnauthorized, "the webhook's signature does not verify")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(w, r)
		})
	}
}

// settle makes each action waiting for acmepay, a page at a time, and
// records acmepay's answer, under its event's id, so the webhook that
// brings the same event changes nothing. Each call to acmepay carries the
// action's id (the attempt's for an intent) as its idempotency key, so a
// run that stops part-way, or two runs at once, make each action once. A
// Refund's refund is asked of the checkout it refunds, so the checkout's
// actions hold it.
func settle(ctx context.Context, database orm.DatabaseInterface, pay *acmepay.Client, logger *zap.Logger) (int, error) {
	made := 0
	var cursor *string
	limit := int64(50)
	for {
		page, err := database.GetCheckoutRepository().Payments().ListPaymentActions(asAdapter(ctx), orm.CheckoutPaymentsListPaymentActionsParams{Provider: provider, Limit: &limit, Cursor: cursor})
		if err != nil {
			return made, fmt.Errorf("list acmepay's actions: %w", err)
		}
		for _, a := range page.Items {
			checkout, err := scalars.ParseUUID(a.Id)
			if err != nil {
				return made, fmt.Errorf("an action of checkout %q: %w", a.Id, err)
			}
			event, err := makeAction(ctx, pay, checkout, a)
			if err != nil {
				return made, fmt.Errorf("%s %s of checkout %s at acmepay: %w", a.Kind, a.Action, a.Id, err)
			}
			if event.ID == "" {
				continue
			}
			if _, err := recordEvent(ctx, database, checkout, event); err != nil {
				if v, ok := vetoOf(err); ok {
					logger.Warn("the checkout refused acmepay's answer", zap.String("checkout", a.Id), zap.String("veto", v.VetoCode))
					continue
				}
				return made, err
			}
			made++
		}
		if page.Next == nil || *page.Next == "" {
			return made, nil
		}
		cursor = page.Next
	}
}

// makeAction makes one action at acmepay and returns the event its answer
// names: none for a cancel of an intent that ended already, which its
// last event recorded.
func makeAction(ctx context.Context, pay *acmepay.Client, checkout db.IdentityUUID, a orm.CheckoutPaymentsListPaymentActionsResultItemsItem) (acmepay.Event, error) {
	reference := checkout.ToUUID().String()
	providerRef := ""
	if a.ProviderRef != nil {
		providerRef = *a.ProviderRef
	}
	switch a.Kind {
	case "create":
		intent, err := pay.CreateIntent(ctx, a.Attempt, acmepay.IntentRequest{Amount: a.Amount, Currency: a.Currency, Reference: reference, Attempt: a.Attempt})
		if err != nil {
			return acmepay.Event{}, err
		}
		return intentEvent(acmepay.EventIntentCreated, intent), nil
	case "void":
		if providerRef == "" {
			// The intent was never made: there is nothing to cancel, and
			// the attempt is cancelled under the action's own id.
			event := acmepay.Event{ID: "void." + a.Action, Type: acmepay.EventIntentCanceled, Created: a.At / 1000}
			event.Data.Object = acmepay.Object{Object: "payment_intent", Amount: a.Amount, Currency: a.Currency, Attempt: a.Attempt, Reference: reference}
			return event, nil
		}
		intent, err := pay.CancelIntent(ctx, a.Action, providerRef)
		if err != nil {
			return acmepay.Event{}, err
		}
		eventType := acmepay.EventIntentCanceled
		if intent.Status == acmepay.StatusFailed {
			eventType = acmepay.EventIntentFailed
		}
		return intentEvent(eventType, intent), nil
	case "refund":
		refundID := ""
		if a.Refund != nil {
			refundID = *a.Refund
		}
		refund, err := pay.CreateRefund(ctx, a.Action, acmepay.RefundRequest{PaymentIntent: providerRef, Amount: a.Amount, Reference: reference, Refund: refundID})
		if err != nil {
			return acmepay.Event{}, err
		}
		eventType := acmepay.EventRefundSucceeded
		if refund.Status != "succeeded" {
			eventType = acmepay.EventRefundFailed
		}
		event := acmepay.Event{ID: refund.Event, Type: eventType, Created: refund.EventCreated}
		event.Data.Object = acmepay.Object{ID: refund.ID, Object: "refund", Amount: refund.Amount, Currency: refund.Currency, Status: refund.Status, PaymentIntent: refund.PaymentIntent, Reference: refund.Reference, Refund: refund.Refund}
		return event, nil
	}
	return acmepay.Event{}, fmt.Errorf("an action of kind %s, which acmepay's adapter does not make", a.Kind)
}

// reconcile asks acmepay about each attempt a held checkout has that is
// still processing, and records what acmepay says, under its intent's last
// event: a payment whose webhook never came is caught here. Payments keeps
// its attempts in a sub-table of the checkout, which no repository reads,
// so the adapter reads them as the core does.
func reconcile(ctx context.Context, database orm.DatabaseInterface, pay *acmepay.Client, checkout *db.Checkout) error {
	attempts, err := subRows(ctx, database, "Checkout", *checkout.Id, "Payments", "attempts")
	if err != nil {
		return err
	}
	for _, row := range attempts {
		var attempt struct {
			Provider    string `json:"provider"`
			ProviderRef string `json:"providerRef"`
			Status      string `json:"status"`
		}
		if err := unmarshalValues(row.Values, &attempt); err != nil {
			return err
		}
		if attempt.Provider != provider || attempt.ProviderRef == "" || (attempt.Status != "processing" && attempt.Status != "requiresAction") {
			continue
		}
		intent, err := pay.GetIntent(ctx, attempt.ProviderRef)
		if err != nil {
			return err
		}
		eventType := map[string]string{
			acmepay.StatusProcessing: acmepay.EventIntentProcessing,
			acmepay.StatusSucceeded:  acmepay.EventIntentSucceeded,
			acmepay.StatusFailed:     acmepay.EventIntentFailed,
			acmepay.StatusCanceled:   acmepay.EventIntentCanceled,
		}[intent.Status]
		if eventType == "" {
			continue
		}
		if _, err := recordEvent(ctx, database, *checkout.Id, intentEvent(eventType, intent)); err != nil {
			if _, ok := vetoOf(err); ok {
				continue
			}
			return err
		}
	}
	return nil
}
