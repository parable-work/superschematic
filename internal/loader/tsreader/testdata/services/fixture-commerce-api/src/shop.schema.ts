import { auth, behaviorOperations } from "@superschematic/api";
import { Checkout, Inventory } from "@schemas/fixture-commerce-db";

// An inventory's stock for any signed-in caller (D70, D103's step 4):
// receiveStock, adjustStock and countStock under stock, which need
// stock.adjust, listStockMovements, and the contract Supply's operations,
// which a caller's direct call is not_requested of unless it changes
// nothing: a claim takes units through its Holds alone.
@behaviorOperations(Inventory)
@auth
export abstract class InventoryStock {}

// A checkout's holds: retakeHolds and listHolds under holds, beside the
// status Workflow's transition, whose moves take, commit, release and
// return what it holds.
@behaviorOperations(Checkout)
@auth
export abstract class CheckoutHolds {}
