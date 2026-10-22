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
	types "example.com/acme/types/go/shop-orders"
	engine "github.com/parable-work/superschematic/runtime/engine/go"
	"go.uber.org/zap"
)

// acmepay's adapter (D70), which is shop-orders' own code: the engine
// opens no connection to a provider. Payments records what is to be done,
// the intents, captures, voids and refunds, as actions; payCheckout and
// the job SettlePayments make them at acmepay, and every outcome, from
// acmepay's answer or its webhook, is recorded with recordPaymentEvent as
// acmepay's event, which applies once per event id.

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
// the intent it is about, with the engine's attempt, or the refund, with
// the engine's refund. No payer's name, email or card leaves acmepay, so
// none is recorded.
func eventParams(event acmepay.Event) (map[string]any, bool) {
	kind, ok := kinds[event.Type]
	if !ok {
		return nil, false
	}
	object := event.Data.Object
	params := map[string]any{
		"provider": provider,
		"eventId":  event.ID,
		"kind":     kind,
		"amount":   object.Amount,
		"currency": object.Currency,
		"at":       event.Created * 1000,
	}
	if object.ID != "" {
		params["providerRef"] = object.ID
	}
	if object.Object == "refund" {
		params["refund"] = object.Refund
	} else if object.Attempt != "" {
		params["attempt"] = object.Attempt
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
// adapter.
func recordEvent(ctx context.Context, b *engine.Background, checkout string, event acmepay.Event) error {
	params, ok := eventParams(event)
	if !ok {
		return nil
	}
	return call(ctx, b, nil, "instances.invoke", adapterPrincipal(), checkoutSchema, checkout, "recordPaymentEvent", params, inNamespace)
}

// PaymentWebhooks implements acmepay's webhook. Its verifier has checked
// the signature by then (Verifier).
type PaymentWebhooks struct {
	Engine *engine.Background
	DB     orm.DatabaseInterface
	Stock  *stocksdk.ShopStockSDK
	Logger *zap.Logger
}

var _ api.PaymentWebhooksImplementation = (*PaymentWebhooks)(nil)

// PaymentEvents records an acmepay event on the checkout its reference
// names, and answers quickly: acmepay sends the event again until it hears
// 2xx. An event of a type the shop does not record, one about no
// checkout, and one a behavior vetoes (a currency_mismatch, say), are
// acknowledged, the last logged, so acmepay does not send them forever;
// a busy or unopened engine is 503, which acmepay tries again. A
// checkout the capture placed writes its order here, if the runner's
// Reactions rule placed it already, else when it is next read or
// SettlePayments runs.
func (w *PaymentWebhooks) PaymentEvents(ctx context.Context, in *types.AcmepayEvent) (*types.PaymentEventReceipt, error) {
	event := acmepay.Event{ID: in.Id, Type: in.Type, Created: int64(in.Created)}
	object := in.Data.Object
	event.Data.Object = acmepay.Object{
		ID: object.Id, Object: object.Object, Amount: int64(object.Amount), Currency: object.Currency, Status: object.Status,
		PaymentIntent: object.PaymentIntent, Reference: object.Reference, Attempt: object.Attempt, Refund: object.Refund,
	}
	logger := w.Logger.With(zap.String("event", in.Id), zap.String("type", in.Type))
	if _, ok := kinds[in.Type]; !ok || event.Data.Object.Reference == "" {
		logger.Info("dropped an acmepay event the shop does not record")
		return &types.PaymentEventReceipt{Received: true}, nil
	}
	err := recordEvent(ctx, w.Engine, event.Data.Object.Reference, event)
	var unavailableErr *unavailableError
	switch {
	case errors.As(err, &unavailableErr):
		return nil, err
	case err != nil:
		if v, ok := vetoOf(err); ok {
			logger.Warn("the checkout refused acmepay's event", zap.String("veto", v.VetoCode), zap.Error(err))
			return &types.PaymentEventReceipt{Received: true}, nil
		}
		var appErr *api.AppError
		if errors.As(err, &appErr) && appErr.HTTPStatus() < 500 {
			logger.Warn("acmepay's event names no checkout the shop can record it on", zap.Error(err))
			return &types.PaymentEventReceipt{Received: true}, nil
		}
		return nil, err
	}
	if kinds[in.Type] == "captured" {
		if rec, err := readCheckout(ctx, w.Engine, adapterPrincipal(), event.Data.Object.Reference); err == nil && rec != nil && rec.Behaviors.Workflow.Status == "placed" {
			if _, err := placeOrder(ctx, w.DB, w.Stock, logger, time.Now(), rec); err != nil {
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

// action is one of listPaymentActions' items: what acmepay is to do.
type action struct {
	Action      string `json:"action"`
	Kind        string `json:"kind"`
	Attempt     string `json:"attempt"`
	Amount      int64  `json:"amount"`
	Currency    string `json:"currency"`
	ID          string `json:"id"`
	ProviderRef string `json:"providerRef"`
	Refund      string `json:"refund"`
	// At is when Payments recorded it, in epoch milliseconds.
	At int64 `json:"at"`
}

// settle makes each action waiting for acmepay, a page at a time, and
// records acmepay's answer, under its event's id, so the webhook that
// brings the same event changes nothing. Each call to acmepay carries the
// action's id (the attempt's for an intent) as its idempotency key, so a
// run that stops part-way, or two runs at once, make each action once. A
// Checkout's actions and a Refund's are the Checkout's: a refund is asked
// of the checkout it refunds.
func settle(ctx context.Context, b *engine.Background, pay *acmepay.Client, logger *zap.Logger) (int, error) {
	made := 0
	cursor := ""
	for {
		params := map[string]any{"provider": provider, "limit": 50}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page struct {
			Items []action `json:"items"`
			Next  *string  `json:"next"`
		}
		if err := call(ctx, b, &page, "instances.invokeSchema", adapterPrincipal(), checkoutSchema, "listPaymentActions", params, inNamespace); err != nil {
			return made, fmt.Errorf("list acmepay's actions: %w", err)
		}
		for _, a := range page.Items {
			event, err := makeAction(ctx, pay, a)
			if err != nil {
				return made, fmt.Errorf("%s %s of checkout %s at acmepay: %w", a.Kind, a.Action, a.ID, err)
			}
			if event.ID == "" {
				continue
			}
			if err := recordEvent(ctx, b, a.ID, event); err != nil {
				if v, ok := vetoOf(err); ok {
					logger.Warn("the checkout refused acmepay's answer", zap.String("checkout", a.ID), zap.String("veto", v.VetoCode))
					continue
				}
				return made, err
			}
			made++
		}
		if page.Next == nil || *page.Next == "" {
			return made, nil
		}
		cursor = *page.Next
	}
}

// makeAction makes one action at acmepay and returns the event its answer
// names: none for a cancel of an intent that ended already, which its
// last event recorded.
func makeAction(ctx context.Context, pay *acmepay.Client, a action) (acmepay.Event, error) {
	switch a.Kind {
	case "create":
		intent, err := pay.CreateIntent(ctx, a.Attempt, acmepay.IntentRequest{Amount: a.Amount, Currency: a.Currency, Reference: a.ID, Attempt: a.Attempt})
		if err != nil {
			return acmepay.Event{}, err
		}
		return intentEvent(acmepay.EventIntentCreated, intent), nil
	case "void":
		if a.ProviderRef == "" {
			// The intent was never made: there is nothing to cancel, and
			// the attempt is cancelled under the action's own id.
			event := acmepay.Event{ID: "void." + a.Action, Type: acmepay.EventIntentCanceled, Created: a.At / 1000}
			event.Data.Object = acmepay.Object{Object: "payment_intent", Amount: a.Amount, Currency: a.Currency, Attempt: a.Attempt, Reference: a.ID}
			return event, nil
		}
		intent, err := pay.CancelIntent(ctx, a.Action, a.ProviderRef)
		if err != nil {
			return acmepay.Event{}, err
		}
		eventType := acmepay.EventIntentCanceled
		if intent.Status == acmepay.StatusFailed {
			eventType = acmepay.EventIntentFailed
		}
		return intentEvent(eventType, intent), nil
	case "refund":
		intent := a.ProviderRef
		refund, err := pay.CreateRefund(ctx, a.Action, acmepay.RefundRequest{PaymentIntent: intent, Amount: a.Amount, Reference: a.ID, Refund: a.Refund})
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

// reconcile asks acmepay about each attempt a checkout holds that is still
// processing, and records what acmepay says, under its intent's last
// event: a payment whose webhook never came is caught here.
func reconcile(ctx context.Context, b *engine.Background, pay *acmepay.Client, rec *checkoutRecord) error {
	for _, a := range rec.Behaviors.Payments.Attempts {
		if a.Provider != provider || a.ProviderRef == "" || (a.Status != "processing" && a.Status != "requiresAction") {
			continue
		}
		intent, err := pay.GetIntent(ctx, a.ProviderRef)
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
		if err := recordEvent(ctx, b, rec.ID, intentEvent(eventType, intent)); err != nil {
			if _, ok := vetoOf(err); ok {
				continue
			}
			return err
		}
	}
	return nil
}
