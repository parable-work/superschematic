package loader

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// The fixtures of D103's step 4's Payments (D70): orders that take
// payments over their own total and refunds of one, and the API that
// serves both.
const (
	paymentsDBFixture  = "tsreader/testdata/services/fixture-payments-db"
	paymentsAPIFixture = "tsreader/testdata/services/fixture-payments-api"
)

// TestPaymentsFixtureLoads: the fixture loads with no warning and no
// error; Order and Refund stay tables, each implementing Payments beside
// its status Workflow, each config read as the TypeScript form wrote it;
// Payments owns its summary columns on both (due, authorized, captured,
// refunded, outstanding, overpaid, currency and state), keeps attempts,
// actions, refunds and issues as sub-tables, each keyed and related to its
// row, and the provider events in a table kept for the table as a whole,
// related to no row; no queue,
// since neither arms a deadline, hears a write or emits an event; and the
// expanded IR is held to its golden (D70, D103's step 4).
func TestPaymentsFixtureLoads(t *testing.T) {
	var warnings []string
	warn := WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })
	db, err := LoadService(filepath.FromSlash(paymentsDBFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("the fixture's warnings =\n%q\nwant none", warnings)
	}
	order, refund := db.Types["Order"], db.Types["Refund"]
	if order == nil || order.IsEngineType() || !order.Optimistic || refund == nil || !refund.Optimistic {
		t.Fatalf("Order = %s, Refund = %s", marshal(t, order), marshal(t, refund))
	}
	uses := func(td *ir.TypeDef) []string {
		var out []string
		for _, ref := range td.Behaviors {
			out = append(out, ref.Name+"@"+ref.Field)
		}
		return out
	}
	for _, td := range []*ir.TypeDef{order, refund} {
		if got, want := uses(td), []string{"Payments@", "Workflow@status"}; !slices.Equal(got, want) {
			t.Errorf("%s's uses = %v, want %v", td.Name, got, want)
		}
	}
	if !strings.Contains(string(order.Behaviors[0].Config), `"require":{"placed":"paid"}`) || !strings.Contains(string(refund.Behaviors[0].Config), `"refundOf":{"link":"order"}`) {
		t.Errorf("the configs = %s and %s", order.Behaviors[0].Config, refund.Behaviors[0].Config)
	}
	for _, td := range []*ir.TypeDef{order, refund} {
		var owned []string
		for _, fd := range td.Fields {
			if fd.Owned() && fd.Behavior.Behavior == "Payments" {
				owned = append(owned, fd.Name)
			}
		}
		want := []string{"paymentsDue", "paymentsAuthorized", "paymentsCaptured", "paymentsRefunded", "paymentsOutstanding", "paymentsOverpaid", "paymentsCurrency", "paymentsState"}
		if !slices.Equal(owned, want) {
			t.Errorf("%s owns %v of Payments', want %v", td.Name, owned, want)
		}
	}
	for _, row := range []string{"Order", "Refund"} {
		for _, sub := range []string{"Attempts", "Actions", "Refunds", "Issues"} {
			td := db.Types[row+"Payments"+sub]
			if td == nil || td.Origin != ir.OriginBehavior || td.BehaviorTable == nil || td.BehaviorTable.RowField == "" {
				t.Fatalf("%sPayments%s = %s", row, sub, marshal(t, td))
			}
		}
		// The provider events, kept for the table as a whole: no row field.
		events := db.Types[row+"PaymentsEvents"]
		if events == nil || events.Origin != ir.OriginBehavior || events.BehaviorTable == nil || events.BehaviorTable.RowField != "" {
			t.Fatalf("%sPaymentsEvents = %s", row, marshal(t, events))
		}
	}
	for _, name := range []string{"Order", "Refund"} {
		for _, kind := range []string{ir.BehaviorQueueDeadlines, ir.BehaviorQueueChanges, ir.BehaviorQueueEvents} {
			if queue := db.BehaviorQueueOf(name, kind); queue != nil {
				t.Errorf("%s's %s queue = %s, want none", name, kind, queue.Name)
			}
		}
	}
	compareExpanded(t, db, "fixture-payments-db.expanded.json")
}

// TestPaymentsAPIFixtureLoads: the API over the orders loads with no
// warning, each table's behaviors' operations served by its
// @behaviorOperations class: Payments' and the Workflow's on an order,
// listPaymentActions on the orders as a whole among them, and on a refund
// (D70, D103's step 4).
func TestPaymentsAPIFixtureLoads(t *testing.T) {
	var warnings []string
	warn := WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })
	api, err := LoadService(filepath.FromSlash(paymentsAPIFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("the fixture's warnings =\n%q\nwant none", warnings)
	}
	var sets []string
	for _, set := range api.OperationSets {
		sets = append(sets, set.Name)
	}
	slices.Sort(sets)
	if want := []string{"OrderPayments", "RefundPayments"}; !slices.Equal(sets, want) {
		t.Errorf("the API's operation sets = %v, want %v", sets, want)
	}
}
