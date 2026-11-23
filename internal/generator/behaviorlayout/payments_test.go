package behaviorlayout_test

import (
	"slices"
	"strings"
	"testing"
)

// paymentsFixture is the DB service whose Order takes payments over its
// own total and whose Refund refunds one (D70, D103's step 4).
const paymentsFixture = "../../loader/tsreader/testdata/services/fixture-payments-db"

// The layout document of tables that implement Payments (D70 on D103's
// step 4): each of Order's and Refund's uses is Payments' under payments,
// beside the status Workflow, owning its summary columns (due, authorized,
// captured, refunded, outstanding, overpaid, currency and state, attempts
// computed at a read); its sub-tables attempts, actions, refunds and
// issues are keyed by their declared keys beside the row's key column,
// and its shared table events by the event alone, with the version each
// write is held to; its operations are the ones a table runs,
// listPaymentActions on the table as a whole; Refund's relation order is
// the own field refundOf reads; neither keeps a queue.
// testdata/fixture-payments-db.layout.json is the document the adapters of
// every language are written against.
func TestLayoutOfPayments(t *testing.T) {
	layout := layoutOfFixture(t, paymentsFixture, "fixture-payments-db")
	for _, name := range []string{"Order", "Refund"} {
		td := layout.Types[name]
		if td == nil || len(td.Uses) != 2 {
			t.Fatalf("the layout's %s = %+v", name, td)
		}
		use := td.Uses[0]
		if use.ID != "Payments" || use.Member != "payments" {
			t.Errorf("%s's first use = %+v, want Payments' under payments", name, use)
		}
		var fields []string
		for _, field := range use.Fields {
			fields = append(fields, field.Field+"="+field.Column.Column)
		}
		if want := "paymentsDue=payments_due, paymentsAuthorized=payments_authorized, paymentsCaptured=payments_captured, paymentsRefunded=payments_refunded, paymentsOutstanding=payments_outstanding, paymentsOverpaid=payments_overpaid, paymentsCurrency=payments_currency, paymentsState=payments_state"; strings.Join(fields, ", ") != want {
			t.Errorf("%s's Payments fields = %s, want %s", name, strings.Join(fields, ", "), want)
		}
		var tables []string
		for _, sub := range use.Tables {
			tables = append(tables, sub.Name+"="+sub.Table+"/"+sub.Key.Column.Column)
		}
		prefix := strings.ToLower(name)
		if want := []string{
			"attempts=" + prefix + "_payments_attempts/attempt",
			"actions=" + prefix + "_payments_actions/action",
			"refunds=" + prefix + "_payments_refunds/refund",
			"issues=" + prefix + "_payments_issues/refund",
		}; !slices.Equal(tables, want) {
			t.Errorf("%s's sub-tables = %v, want %v", name, tables, want)
		}
		if len(use.Shared) != 1 || use.Shared[0].Table != prefix+"_payments_events" || use.Shared[0].Key.Column.Column != "event" || use.Shared[0].Version == "" {
			t.Errorf("%s's shared tables = %+v", name, use.Shared)
		}
		var operations []string
		for _, op := range use.Operations {
			name := op.Name
			if op.Table {
				name += "(table)"
			}
			operations = append(operations, name)
		}
		if want := "openPayment, recordPaymentEvent, capturePayment, voidPayment, refundPayment, listPaymentActions(table), refundFor, settleRefundFor"; strings.Join(operations, ", ") != want {
			t.Errorf("%s's operations = %s, want %s", name, strings.Join(operations, ", "), want)
		}
		if td.Deadlines != nil || td.Changes != nil || td.Events != nil {
			t.Errorf("%s's queues = %+v %+v %+v, want none", name, td.Deadlines, td.Changes, td.Events)
		}
	}
	relation := ""
	for _, field := range layout.Types["Refund"].Spec.Fields {
		if field.Relation != nil {
			relation = field.Name + "->" + field.Relation.Name + ":" + field.Relation.Type
		}
	}
	if want := "orderId->order:Order"; relation != want {
		t.Errorf("Refund's relation = %s, want %s", relation, want)
	}
}
