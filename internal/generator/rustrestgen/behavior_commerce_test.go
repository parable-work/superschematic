package rustrestgen

import (
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// The fixture of Stock and Holds (D70 on D103's step 4):
// fixture-commerce-api serves InventoryStock and CheckoutHolds,
// @behaviorOperations of Inventory and Checkout for any signed-in caller,
// over fixture-commerce-db, its one DB dependency. Each is a route of the
// row under its use's member, run through the behaviors' core with
// run_operation, the contract Supply's among Stock's; the API runs no
// worker.
var commerceFixture = behaviorFixture{api: "fixture-commerce-api", db: "fixture-commerce-db"}

// TestWriteRustAPIGoldenCommerce pins the crate of fixture-commerce-api:
// the router mounts each of Stock's and Holds' operations, and the layout
// it embeds holds each use's sub-tables. Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenCommerce -update
func TestWriteRustAPIGoldenCommerce(t *testing.T) {
	_, db, reg := commerceFixture.load(t)
	output := commerceFixture.generate(t, db)
	deps := behaviorDeps(behaviorLayout(t, db, reg))
	deps.Database = commerceFixture.db
	deps.DatabaseTypesCrate = naming.Default().RustTypesCrate(commerceFixture.db)
	deps.DatabaseTypesPath = "../../types/rust/" + commerceFixture.db
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
	if want := []string{
		"checkout-holds.holdsListHolds POST /api/checkout/{id}/holds/list-holds Checkout.Holds.listHolds",
		"checkout-holds.holdsRetakeHolds POST /api/checkout/{id}/holds/retake-holds Checkout.Holds.retakeHolds",
		"checkout-holds.statusTransition POST /api/checkout/{id}/status/transition Checkout.status.transition",
		"inventory-stock.stockAdjustStock POST /api/inventory/{id}/stock/adjust-stock Inventory.Stock.adjustStock",
		"inventory-stock.stockCheckHold POST /api/inventory/{id}/stock/check-hold Inventory.Stock.checkHold",
		"inventory-stock.stockCommitFor POST /api/inventory/{id}/stock/commit-for Inventory.Stock.commitFor",
		"inventory-stock.stockCountStock POST /api/inventory/{id}/stock/count-stock Inventory.Stock.countStock",
		"inventory-stock.stockHoldFor POST /api/inventory/{id}/stock/hold-for Inventory.Stock.holdFor",
		"inventory-stock.stockListStockMovements POST /api/inventory/{id}/stock/list-stock-movements Inventory.Stock.listStockMovements",
		"inventory-stock.stockReceiveStock POST /api/inventory/{id}/stock/receive-stock Inventory.Stock.receiveStock",
		"inventory-stock.stockReleaseFor POST /api/inventory/{id}/stock/release-for Inventory.Stock.releaseFor",
		"inventory-stock.stockReturnFor POST /api/inventory/{id}/stock/return-for Inventory.Stock.returnFor",
	}; !slices.Equal(behaviors, want) {
		t.Errorf("behavior operations\n%q, want\n%q", behaviors, want)
	}
	if len(output.Workers) != 0 || len(output.BehaviorWorkers) != 0 {
		t.Errorf("workers %+v and behavior workers %+v, want none", output.Workers, output.BehaviorWorkers)
	}
	generated := writeGoldenAPI(t, commerceFixture.api, output)
	for file, want := range map[string][]string{
		"src/behaviors.rs": {
			`pub const RULES_LAYOUT: &str = r#"{`,
			`"inventory_stock_holds"`,
			`"inventory_stock_movements"`,
			`"checkout_holds_items"`,
		},
	} {
		for _, text := range want {
			if !strings.Contains(generated[file], text) {
				t.Errorf("%s does not contain %q", file, text)
			}
		}
	}
}
