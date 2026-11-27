package shoporders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	api "example.com/acme/api/shop-orders"
	engine "github.com/parable-work/superschematic/runtime/engine/go"
	"go.uber.org/zap"
)

// The engine types shop-orders keeps, of shop-db (D70).
const (
	checkoutSchema = "Checkout"
	refundSchema   = "Refund"
	// provider is acmepay's name in Payments' attempts and actions.
	provider = "acmepay"
)

// serviceName is the deployable shop-orders' in-process calls name to the
// engine's policy (D37): shop-db's rules let shop-orders read and write.
const serviceName = "shop-orders"

// shopperPrincipal is a shopper's call through shop-orders: the shopper is
// the actor its events record, and shop-orders the service that made it.
func shopperPrincipal(subject string) *engine.Principal {
	return &engine.Principal{
		Subject: subject,
		Service: &engine.PrincipalService{Deployable: serviceName, Serves: []string{serviceName}, Subject: serviceName},
	}
}

// adapterPrincipal is shop-orders as acmepay's adapter, with no shopper:
// it records what acmepay did, which Payments' record permission allows,
// and asks refunds of a checkout (refunds' permission).
func adapterPrincipal() *engine.Principal {
	return &engine.Principal{
		Subject:     serviceName,
		Permissions: []string{"payments.record", "payments.refund"},
		Service:     &engine.PrincipalService{Deployable: serviceName, Serves: []string{serviceName}, Subject: serviceName},
	}
}

// engineOf waits for the engine Deps holds, which the entrypoint opens in
// the background, for as long as the request's context lets it.
func engineOf(ctx context.Context, b *engine.Background) (*engine.Engine, error) {
	if b == nil {
		return nil, unavailable("shop-db's engine is not configured")
	}
	e, err := b.Wait(ctx)
	if err != nil {
		return nil, unavailable("shop-db's engine is not open yet")
	}
	return e, nil
}

// call makes an engine call and decodes its result into out, nil to drop
// it; a refusal becomes the API's error (refusal).
func call(ctx context.Context, b *engine.Background, out any, op string, principal *engine.Principal, args ...any) error {
	e, err := engineOf(ctx, b)
	if err != nil {
		return err
	}
	result, err := e.Call(ctx, op, principal, args...)
	if err != nil {
		return refusal(err)
	}
	if out == nil || len(result) == 0 {
		return nil
	}
	if err := json.Unmarshal(result, out); err != nil {
		return fmt.Errorf("read the engine's answer to %s: %w", op, err)
	}
	return nil
}

// veto is what a behavior said when it refused a call: its code, which a
// client branches on, and its details.
type veto struct {
	Behavior    string          `json:"behavior"`
	VetoCode    string          `json:"vetoCode"`
	VetoDetails json.RawMessage `json:"vetoDetails"`
}

// vetoOf is the veto err carries, when it is one.
func vetoOf(err error) (veto, bool) {
	var refused *engine.Error
	if !errors.As(err, &refused) || refused.Code != engine.CodeVetoed {
		return veto{}, false
	}
	var v veto
	_ = json.Unmarshal(refused.Details, &v)
	return v, v.VetoCode != ""
}

// refusal answers an engine error as the API's: a behavior's veto is 409
// with its code and details (unavailable, holds_missing, nothing_due, ...),
// a refused argument 400, a missing instance 404, busy or not open 503, and
// anything else the server's.
func refusal(err error) error {
	var refused *engine.Error
	if !errors.As(err, &refused) {
		return err
	}
	switch refused.Code {
	case engine.CodeVetoed:
		v, _ := vetoOf(err)
		conflict := api.ConflictError(refused.Message, err).WithCode(v.VetoCode)
		if len(v.VetoDetails) > 0 && string(v.VetoDetails) != "null" {
			conflict = conflict.WithDetails(v.VetoDetails)
		}
		return conflict
	case engine.CodeNotFound:
		return api.NotFoundError("checkout", err)
	case engine.CodeInvalidArgument, engine.CodeInvalidInstance:
		return api.BadRequestError(refused.Message, err)
	case engine.CodeConflict, engine.CodeSeqMismatch:
		return api.ConflictError(refused.Message, err)
	case engine.CodeBusy, engine.CodeUnavailable:
		return unavailable(refused.Message)
	}
	return err
}

// unavailableError is a 503, which a client tries again: the engine is not
// open yet, or busy, or acmepay did not answer.
type unavailableError struct{ message string }

func unavailable(message string) error { return &unavailableError{message: message} }

func (e *unavailableError) Error() string   { return e.message }
func (e *unavailableError) HTTPStatus() int { return http.StatusServiceUnavailable }
func (e *unavailableError) LogFields() []zap.Field {
	return []zap.Field{zap.String("reason", e.message)}
}
func (e *unavailableError) StructuredBody() any {
	return map[string]any{
		"type":   "about:blank",
		"title":  http.StatusText(http.StatusServiceUnavailable),
		"status": http.StatusServiceUnavailable,
		"detail": e.message,
		"code":   "unavailable",
	}
}
