import { Generic, Identity, Temporal } from "superscalar";
import { Nullable, Validate, display, docs } from "@superschematic/schema";
import {
  Authenticated,
  HttpMethod,
  QueryParam,
  auth,
  bodyLimit,
  publicRoute,
  rateLimit,
  requirePermission,
  rest,
  source,
  timeout,
  virtual
} from "@superschematic/api";
import { Order, OrderLine, OrderStatus, Review, ShippingAddress } from "@acme/shop-db";

// One line of an order as a caller sees it.
@source(OrderLine)
export abstract class OrderLineView {
  id: Identity.UUID;
  quantity: Generic.Int64;
  unitPriceCents: Generic.Int64;

  // The product this line is for.
  @virtual
  productId: Identity.UUID;
}

// An order as a caller sees it.
@source(Order)
@display({ noun: "Order", plural: "Orders", summaryFields: ["id", "placedAt", "status", "totalCents"] })
export abstract class OrderView {
  id: Identity.UUID;
  status: OrderStatus;
  placedAt: Temporal.DateTime;
  shippingAddress: ShippingAddress;

  // Why the order was cancelled, when it was.
  cancelReason: Nullable<string>;

  @virtual
  lines: OrderLineView[];

  // The sum of each line's quantity times its unit price.
  @virtual
  totalCents: Generic.Int64;
}

// One line of an order a shopper places: a product and how many.
@display({ noun: "Line", createLabel: "Add a line" })
export abstract class PlaceOrderLine {
  @docs({ title: "Product" })
  productId: Identity.UUID;
  quantity: Validate<number, { min: 1; max: 99 }>;
}

export abstract class PlaceOrderInput {
  lines: Validate<PlaceOrderLine[], { listMin: 1; listMax: 50 }>;
  shippingAddress: ShippingAddress;
}

// A review as anyone sees it.
@source(Review)
@display({ noun: "Review", plural: "Reviews", titleField: "title", summaryFields: ["title", "rating", "createdAt"] })
export abstract class ReviewView {
  id: Identity.UUID;
  rating: number;
  title: string;
  body: string;
  createdAt: Temporal.DateTime;
}

export abstract class WriteReviewInput {
  rating: Validate<number, { min: 1; max: 5 }>;
  title: Validate<string, { minLength: 1; maxLength: 120 }>;
  body: Validate<string, { maxLength: 5000 }>;
}

// Reading orders.
export class OrderQueries extends Authenticated {
  @rest(HttpMethod.GET, "orders/{id}")
  @requirePermission(["orders.read"])
  getOrder(id: Identity.UUID): OrderView {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "orders")
  @requirePermission(["orders.read"])
  listOrders(
    statuses: QueryParam<Nullable<OrderStatus[]>>,
    limit: QueryParam<Nullable<Validate<number, { min: 1; max: 100 }>>>
  ): OrderView[] {
    throw new Error("schema declaration only");
  }
}

@rateLimit({ requestsPerMinute: 60 })
export class OrderMutations extends Authenticated {
  @rest(HttpMethod.POST, "orders")
  @requirePermission(["orders.write"])
  @timeout({ seconds: 10 })
  @bodyLimit({ megabytes: 1 })
  placeOrder(input: PlaceOrderInput): OrderView {
    throw new Error("schema declaration only");
  }

  // Cancels an order that has not shipped, recording why.
  @rest(HttpMethod.POST, "orders/{id}/cancel")
  @requirePermission(["orders.write"])
  cancelOrder(id: Identity.UUID, reason: Nullable<Validate<string, { maxLength: 500 }>>): OrderView {
    throw new Error("schema declaration only");
  }
}

// A product's reviews: anyone may read them, and a signed-in shopper may
// write one.
export class ProductReviews {
  @rest(HttpMethod.GET, "products/{productId}/reviews")
  @publicRoute
  listReviews(productId: Identity.UUID, minRating: QueryParam<Nullable<number>>): ReviewView[] {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.POST, "products/{productId}/reviews")
  @auth
  writeReview(productId: Identity.UUID, input: WriteReviewInput): ReviewView {
    throw new Error("schema declaration only");
  }
}
