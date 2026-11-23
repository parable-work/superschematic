package loader

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// The fixtures of D103's step 4's Stock and Holds (D70): a shop whose
// Inventory Stock counts and whose Checkout holds its lines and its gift
// wrap through the contract Supply, and the API that serves both.
const (
	commerceDBFixture  = "tsreader/testdata/services/fixture-commerce-db"
	commerceAPIFixture = "tsreader/testdata/services/fixture-commerce-api"
)

// TestCommerceFixtureLoads: the fixture loads with no warning and no error;
// Inventory and Checkout stay tables, Inventory implementing Stock and
// Checkout Holds beside its status Workflow, each config read as the
// TypeScript form wrote it; the loader adds Stock's owned onHand and held
// and Holds' owned state and total, and each use's sub-tables, Stock's
// records and movements and Holds' items, each keyed and related to its
// row, and no queue, since neither arms a deadline, hears a write or emits
// an event; and the expanded IR is held to its golden (D70, D103's step 4).
func TestCommerceFixtureLoads(t *testing.T) {
	var warnings []string
	warn := WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })
	db, err := LoadService(filepath.FromSlash(commerceDBFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("the fixture's warnings =\n%q\nwant none", warnings)
	}
	inventory, checkout := db.Types["Inventory"], db.Types["Checkout"]
	if inventory == nil || inventory.IsEngineType() || !inventory.Optimistic || checkout == nil || !checkout.Optimistic {
		t.Fatalf("Inventory = %s, Checkout = %s", marshal(t, inventory), marshal(t, checkout))
	}
	uses := func(td *ir.TypeDef) []string {
		var out []string
		for _, ref := range td.Behaviors {
			out = append(out, ref.Name+"@"+ref.Field)
		}
		return out
	}
	if got, want := uses(inventory), []string{"Stock@"}; !slices.Equal(got, want) {
		t.Errorf("Inventory's uses = %v, want %v", got, want)
	}
	if got, want := uses(checkout), []string{"Holds@", "Workflow@status"}; !slices.Equal(got, want) {
		t.Errorf("Checkout's uses = %v, want %v", got, want)
	}
	if !strings.Contains(string(inventory.Behaviors[0].Config), `"currency":"GBP"`) || !strings.Contains(string(checkout.Behaviors[0].Config), `"link":"wrap"`) {
		t.Errorf("the configs = %s and %s", inventory.Behaviors[0].Config, checkout.Behaviors[0].Config)
	}
	owned := func(td *ir.TypeDef) []string {
		var out []string
		for _, fd := range td.Fields {
			if fd.Owned() {
				out = append(out, fd.Name+":"+fd.TypeRef.Name)
			}
		}
		return out
	}
	if got, want := owned(inventory), []string{"stockOnHand:Generic.Int64", "stockHeld:Generic.Int64"}; !slices.Equal(got, want) {
		t.Errorf("Inventory's owned fields = %v, want %v", got, want)
	}
	if got, want := owned(checkout), []string{"holdsState:string", "holdsTotal:Generic.JSON", "status:string", "statusEnteredAt:Temporal.DateTime"}; !slices.Equal(got, want) {
		t.Errorf("Checkout's owned fields = %v, want %v", got, want)
	}
	for _, sub := range []string{"InventoryStockHolds", "InventoryStockMovements", "CheckoutHoldsItems"} {
		td := db.Types[sub]
		if td == nil || td.Origin != ir.OriginBehavior || td.BehaviorTable == nil || td.BehaviorTable.RowField == "" {
			t.Fatalf("%s = %s", sub, marshal(t, td))
		}
	}
	for _, name := range []string{"Inventory", "Checkout"} {
		for _, kind := range []string{ir.BehaviorQueueDeadlines, ir.BehaviorQueueChanges, ir.BehaviorQueueEvents} {
			if queue := db.BehaviorQueueOf(name, kind); queue != nil {
				t.Errorf("%s's %s queue = %s, want none", name, kind, queue.Name)
			}
		}
	}
	compareExpanded(t, db, "fixture-commerce-db.expanded.json")
}

// TestCommerceAPIFixtureLoads: the API over the shop loads with no warning,
// each table's behaviors' operations served by its @behaviorOperations
// class: Stock's and the contract's on an inventory, Holds' and the
// Workflow's on a checkout (D70, D103's step 4).
func TestCommerceAPIFixtureLoads(t *testing.T) {
	var warnings []string
	warn := WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })
	api, err := LoadService(filepath.FromSlash(commerceAPIFixture), warn)
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
	if want := []string{"CheckoutHolds", "InventoryStock"}; !slices.Equal(sets, want) {
		t.Errorf("the API's operation sets = %v, want %v", sets, want)
	}
}
