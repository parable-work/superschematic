package behaviorlayout_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// commerceFixture is the DB service whose Inventory Stock counts and whose
// Checkout holds its lines and its gift wrap through Supply (D70, D103's
// step 4).
const commerceFixture = "../../loader/tsreader/testdata/services/fixture-commerce-db"

// The layout document of tables that implement Stock and Holds (D70 on
// D103's step 4): Inventory's one use is Stock's, with its owned onHand
// and held, integer columns, its sub-tables holds (keyed by the record)
// and movements (keyed by the sequence), and its operations, the
// contract's among them; Checkout's are Holds', with its owned state and
// total, and its sub-table items keyed by the hold and index, and the
// status Workflow's; the spec gives Checkout's relation wrap as the own
// field that holds its key, which a link hold reads; neither keeps a
// queue. testdata/fixture-commerce-db.layout.json is the document the
// adapters of every language are written against.
func TestLayoutOfStockAndHolds(t *testing.T) {
	layout := layoutOfFixture(t, commerceFixture, "fixture-commerce-db")
	inventory := layout.Types["Inventory"]
	if inventory == nil || len(inventory.Uses) != 1 {
		t.Fatalf("Inventory = %+v", inventory)
	}
	stock := inventory.Uses[0]
	owned, err := json.Marshal(stock.Fields)
	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"name":"onHand","field":"stockOnHand","column":"stock_on_hand","type":"Generic.Int64"},{"name":"held","field":"stockHeld","column":"stock_held","type":"Generic.Int64"}]`; string(owned) != want {
		t.Errorf("Stock's fields = %s, want %s", owned, want)
	}
	var tables []string
	for _, table := range stock.Tables {
		tables = append(tables, table.Name+"="+table.Table+"/"+table.Key.Column.Column)
	}
	if want := "holds=inventory_stock_holds/record, movements=inventory_stock_movements/seq"; strings.Join(tables, ", ") != want {
		t.Errorf("Stock's tables = %s, want %s", strings.Join(tables, ", "), want)
	}
	var operations []string
	for _, op := range stock.Operations {
		operations = append(operations, op.Name)
	}
	if want := "receiveStock, adjustStock, countStock, holdFor, commitFor, releaseFor, returnFor, checkHold, listStockMovements"; strings.Join(operations, ", ") != want {
		t.Errorf("Stock's operations = %s, want %s", strings.Join(operations, ", "), want)
	}
	checkout := layout.Types["Checkout"]
	if checkout == nil || len(checkout.Uses) != 2 {
		t.Fatalf("Checkout = %+v", checkout)
	}
	holds := checkout.Uses[0]
	owned, err = json.Marshal(holds.Fields)
	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"name":"state","field":"holdsState","column":"holds_state","type":"string"},{"name":"total","field":"holdsTotal","column":"holds_total","type":"Generic.JSON"}]`; string(owned) != want {
		t.Errorf("Holds' fields = %s, want %s", owned, want)
	}
	if len(holds.Tables) != 1 || holds.Tables[0].Table != "checkout_holds_items" || holds.Tables[0].Key.Column.Column != "key" {
		t.Errorf("Holds' tables = %+v", holds.Tables)
	}
	relation := ""
	for _, field := range checkout.Spec.Fields {
		if field.Relation != nil {
			relation = field.Name + "->" + field.Relation.Name + ":" + field.Relation.Type
		}
	}
	if want := "wrapId->wrap:Inventory"; relation != want {
		t.Errorf("Checkout's relation = %s, want %s", relation, want)
	}
	for _, td := range []string{"Inventory", "Checkout"} {
		if table := layout.Types[td]; table.Deadlines != nil || table.Changes != nil || table.Events != nil {
			t.Errorf("%s's queues = %+v %+v %+v, want none", td, table.Deadlines, table.Changes, table.Events)
		}
	}
}
