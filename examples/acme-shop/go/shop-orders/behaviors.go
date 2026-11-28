package shoporders

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	api "example.com/acme/api/shop-orders"
	orm "example.com/acme/orm/shop-db"
	db "example.com/acme/types/go/shop-db"
	rules "github.com/parable-work/superschematic/runtime/rules/go"
	rulespostgres "github.com/parable-work/superschematic/runtime/rules/go/postgres"
	rulessqlite "github.com/parable-work/superschematic/runtime/rules/go/sqlite"
	"go.uber.org/zap"
)

// provider is acmepay's name in Payments' attempts and actions (D70).
const provider = "acmepay"

// serviceName is the subject shop-orders' own commands run as, with no
// shopper: acmepay's adapter.
const serviceName = "shop-orders"

// asAdapter is ctx with Checkout's and Refund's behaviors run as shop-orders
// as acmepay's adapter, with no shopper (D70, D103): it records what
// acmepay did, which Payments' record permission allows. The core checks a
// behavior's permission against the caller's, so an operation a shopper's
// call makes, a move or a payment opened, runs as the shopper, whom the
// route's context names, holding none.
func asAdapter(ctx context.Context) context.Context {
	return orm.WithBehaviorCaller(ctx, rules.Caller{Subject: serviceName, Permissions: []string{"payments.record", "payments.refund"}})
}

// veto is what a behavior said when it refused a command: its code, which
// a client branches on, and its details.
type veto struct {
	Behavior    string          `json:"behavior"`
	VetoCode    string          `json:"vetoCode"`
	VetoDetails json.RawMessage `json:"vetoDetails"`
}

// vetoOf is the veto err carries, when it is one.
func vetoOf(err error) (veto, bool) {
	refused, ok := rules.AsRefusal(err)
	if !ok || refused.Code != rules.CodeVetoed {
		return veto{}, false
	}
	var v veto
	_ = json.Unmarshal(refused.Details, &v)
	return v, v.VetoCode != ""
}

// refusal answers a refusal of the behaviors' core as the API's: a
// behavior's veto is 409 with its code and details (unavailable,
// holds_missing, nothing_due, ...), a permission the caller lacks 403, a
// refused argument 400, a missing row 404, a row that kept moving 503, and
// anything else the server's.
func refusal(err error) error {
	refused, ok := rules.AsRefusal(err)
	if !ok {
		if errors.Is(err, orm.ErrNotFound) {
			return api.NotFoundError("checkout", err)
		}
		return err
	}
	switch refused.Code {
	case rules.CodeVetoed:
		v, _ := vetoOf(err)
		conflict := api.ConflictError(refused.Message, err).WithCode(v.VetoCode)
		if len(v.VetoDetails) > 0 && string(v.VetoDetails) != "null" {
			conflict = conflict.WithDetails(v.VetoDetails)
		}
		return conflict
	case rules.CodeForbidden:
		return api.ForbiddenError(err)
	case rules.CodeNotFound:
		return api.NotFoundError("checkout", err)
	case rules.CodeInvalidArgument, rules.CodeInvalidInstance:
		return api.BadRequestError(refused.Message, err)
	case rules.CodeConflict:
		return api.ConflictError(refused.Message, err)
	case rules.CodeBusy:
		return unavailable(refused.Message)
	}
	return err
}

// subRows reads the rows of a sub-table of a behavior's use on one row
// (D103), such as Checkout's Payments attempts, which no repository reads:
// through the behaviors' core's store of the database's dialect, in a
// transaction of its own.
func subRows(ctx context.Context, database orm.DatabaseInterface, typeName string, key db.IdentityUUID, useID, table string) ([]rules.SubRow, error) {
	var rows []rules.SubRow
	err := database.Transaction(ctx, func(tx orm.TxInterface) error {
		store := rulespostgres.NewStore(tx)
		if lite, ok := tx.(interface{ SQL() *sql.Tx }); ok {
			store = rulessqlite.NewStore(lite.SQL())
		}
		var err error
		rows, err = rules.ReadSubRows(ctx, rules.Host{Layout: orm.RulesLayout, Store: store}, typeName, key.String(), useID, table)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("read %s %s's %s %s: %w", typeName, key, useID, table, err)
	}
	return rows, nil
}

// unmarshalValues decodes a sub-table row's values, by field name, into out.
func unmarshalValues(values map[string]json.RawMessage, out any) error {
	data, err := json.Marshal(values)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// unavailableError is a 503, which a client tries again: a row kept
// moving while a command ran, or acmepay did not answer.
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
