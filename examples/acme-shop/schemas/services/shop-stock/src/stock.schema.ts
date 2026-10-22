import { Generic, Identity } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import {
  Authenticated,
  HttpMethod,
  publicRoute,
  requirePermission,
  requireService,
  rest
} from "@superschematic/api";
import { ShopOrders } from "@acme/shop-orders";

// How many units of a product the warehouse holds: shop-db's Inventory of
// the product, kept by the engine's Stock behavior (D70), or none, for a
// product the warehouse has never stocked. onHand is on the shelf, held
// what checkouts hold of it until they are paid for or let go, and
// available what is left to sell. A product is low once available falls to
// its reorderAt.
export abstract class StockView {
  productId: Identity.UUID;
  onHand: Generic.Int64;
  held: Generic.Int64;
  available: Generic.Int64;
  reorderAt: Nullable<Generic.Int64>;
  low: boolean;
}

// A count of the shelf, with the level at which the product is low: one
// left out keeps the Inventory's own, none for a product never stocked;
// zero turns it off.
export abstract class SetStockInput {
  quantity: Validate<Generic.Int64, { min: 0 }>;
  reorderAt: Nullable<Validate<Generic.Int64, { min: 0 }>>;
}

// A delivery booked in.
export abstract class ReceiveStockInput {
  quantity: Validate<Generic.Int64, { min: 1 }>;
  reason: Validate<string, { minLength: 1; maxLength: 200 }>;
}

// One line of a placed checkout: a product and how many of it sold.
export abstract class SoldLine {
  productId: Identity.UUID;
  quantity: Validate<Generic.Int64, { min: 1; max: 99 }>;
}

export abstract class StockSoldInput {
  lines: Validate<SoldLine[], { listMin: 1; listMax: 50 }>;
}

// Reading stock: anyone may.
export class StockQueries {
  @rest(HttpMethod.GET, "stock")
  @publicRoute
  listStock(): StockView[] {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "stock/{productId}")
  @publicRoute
  getStock(productId: Identity.UUID): StockView {
    throw new Error("schema declaration only");
  }
}

// Staff who hold stock.write count a product's shelf, which stocks a
// product the warehouse never held, and set the level it is low at, and
// book deliveries in. Each is a movement of the Inventory's Stock, with
// its reason, which the engine keeps.
export class StockMutations extends Authenticated {
  @rest(HttpMethod.PUT, "stock/{productId}")
  @requirePermission(["stock.write"])
  setStock(productId: Identity.UUID, input: SetStockInput): StockView {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.POST, "stock/{productId}/receipts")
  @requirePermission(["stock.write"])
  receiveStock(productId: Identity.UUID, input: ReceiveStockInput): StockView {
    throw new Error("schema declaration only");
  }
}

// What a placed checkout sold, which only shop-orders reports, its server
// or its job, with no shopper: a product the sale left low enqueues
// StockLow, which the worker ReorderStock handles. The checkout's Holds
// took the units off the shelf already; this reads them.
export class StockSales {
  @rest(HttpMethod.POST, "stock/sold")
  @requireService({ from: [ShopOrders] })
  stockSold(input: StockSoldInput): StockView[] {
    throw new Error("schema declaration only");
  }
}
