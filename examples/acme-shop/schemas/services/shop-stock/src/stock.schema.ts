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

// How many units of a product the warehouse holds: a row of shop-db's
// StockLevel table, or none, for a product the warehouse has never stocked.
// A reservation that takes the quantity below the threshold files a
// restock request for the product.
export abstract class StockView {
  productId: Identity.UUID;
  quantity: Generic.Int64;
  threshold: Nullable<Generic.Int64>;
}

// A threshold left out keeps the level's own, none for a product never
// stocked; zero turns it off.
export abstract class SetStockInput {
  quantity: Validate<Generic.Int64, { min: 0 }>;
  threshold: Nullable<Validate<Generic.Int64, { min: 0 }>>;
}

// One line of a reservation: a product and how many of it.
export abstract class ReserveStockLine {
  productId: Identity.UUID;
  quantity: Validate<Generic.Int64, { min: 1; max: 99 }>;
}

export abstract class ReserveStockInput {
  lines: Validate<ReserveStockLine[], { listMin: 1; listMax: 50 }>;
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

// Staff who hold stock.write set how many units of a product the
// warehouse holds, and the threshold it is restocked below.
export class StockMutations extends Authenticated {
  @rest(HttpMethod.PUT, "stock/{productId}")
  @requirePermission(["stock.write"])
  setStock(productId: Identity.UUID, input: SetStockInput): StockView {
    throw new Error("schema declaration only");
  }
}

// Reserving stock takes it off the shelf, for every line or for none: a
// line the warehouse cannot fill is refused 409. Only shop-orders' server
// reserves, placing an order for the shopper it forwards, who must hold
// orders.write, as placing the order needs. A line that takes a product's
// level below its threshold enqueues StockLow, which the worker
// ReorderStock handles.
export class StockReservations {
  @rest(HttpMethod.POST, "stock/reservations")
  @requirePermission(["orders.write"])
  @requireService({ from: [ShopOrders] })
  reserveStock(input: ReserveStockInput): StockView[] {
    throw new Error("schema declaration only");
  }
}
