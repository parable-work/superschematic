package rustrestgen

import (
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// The fixture of Payments (D70 on D103's step 4): fixture-payments-api
// serves OrderPayments and RefundPayments, @behaviorOperations of Order
// and Refund for any signed-in caller, over fixture-payments-db, its one
// DB dependency. Each is a route of the row under its use's member, run
// through the behaviors' core with run_operation, refundFor and
// settleRefundFor among them, and listPaymentActions a route of the table
// as a whole; the API runs no worker.
var paymentsFixture = behaviorFixture{api: "fixture-payments-api", db: "fixture-payments-db"}

// TestWriteRustAPIGoldenPayments pins the crate of fixture-payments-api:
// the router mounts each of Payments' operations and the Workflows', and
// the layout it embeds holds each use's sub-tables and its shared table of
// provider events. Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenPayments -update
func TestWriteRustAPIGoldenPayments(t *testing.T) {
	_, db, reg := paymentsFixture.load(t)
	output := paymentsFixture.generate(t, db)
	deps := behaviorDeps(behaviorLayout(t, db, reg))
	deps.Database = paymentsFixture.db
	deps.DatabaseTypesCrate = naming.Default().RustTypesCrate(paymentsFixture.db)
	deps.DatabaseTypesPath = "../../types/rust/" + paymentsFixture.db
	if err := output.SetDeps(deps); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	var behaviors []string
	for _, endpoint := range output.Endpoints {
		if b := endpoint.Behavior; b != nil {
			behaviors = append(behaviors, endpoint.Namespace+"."+endpoint.Name+" "+strings.ToUpper(endpoint.Method)+" "+endpoint.Path+" "+b.Type+"."+b.Use+"."+b.Operation)
		}
	}
	slices.Sort(behaviors)
	var want []string
	for _, table := range []struct{ set, path, name string }{{"order-payments", "order", "Order"}, {"refund-payments", "refund", "Refund"}} {
		for _, op := range []struct{ member, path, name string }{
			{"paymentsCapturePayment", "{id}/payments/capture-payment", "capturePayment"},
			{"paymentsListPaymentActions", "payments/list-payment-actions", "listPaymentActions"},
			{"paymentsOpenPayment", "{id}/payments/open-payment", "openPayment"},
			{"paymentsRecordPaymentEvent", "{id}/payments/record-payment-event", "recordPaymentEvent"},
			{"paymentsRefundFor", "{id}/payments/refund-for", "refundFor"},
			{"paymentsRefundPayment", "{id}/payments/refund-payment", "refundPayment"},
			{"paymentsSettleRefundFor", "{id}/payments/settle-refund-for", "settleRefundFor"},
			{"paymentsVoidPayment", "{id}/payments/void-payment", "voidPayment"},
		} {
			want = append(want, table.set+"."+op.member+" POST /api/"+table.path+"/"+op.path+" "+table.name+".Payments."+op.name)
		}
		want = append(want, table.set+".statusTransition POST /api/"+table.path+"/{id}/status/transition "+table.name+".status.transition")
	}
	if !slices.Equal(behaviors, want) {
		t.Errorf("behavior operations\n%q, want\n%q", behaviors, want)
	}
	if len(output.Workers) != 0 || len(output.BehaviorWorkers) != 0 {
		t.Errorf("workers %+v and behavior workers %+v, want none", output.Workers, output.BehaviorWorkers)
	}
	generated := writeGoldenAPI(t, paymentsFixture.api, output)
	for file, want := range map[string][]string{
		"src/behaviors.rs": {
			`pub const RULES_LAYOUT: &str = r#"{`,
			`"order_payments_attempts"`,
			`"order_payments_actions"`,
			`"order_payments_refunds"`,
			`"order_payments_issues"`,
			`"order_payments_events"`,
			`"refund_payments_issues"`,
			`"refund_payments_events"`,
		},
	} {
		for _, text := range want {
			if !strings.Contains(generated[file], text) {
				t.Errorf("%s does not contain %q", file, text)
			}
		}
	}
}
