import { Generic, Identity, Temporal } from "superscalar";
import { Nullable, Validate, display, docs } from "@superschematic/schema";
import {
  Authenticated,
  HttpMethod,
  QueryParam,
  RefersTo,
  SearchQuery,
  auth,
  bodyLimit,
  paginated,
  publicRoute,
  rateLimit,
  requireOwnership,
  requirePermission,
  rest,
  source,
  timeout,
  virtual
} from "@superschematic/api";
import { Order, OrderLine, OrderStatus, Product, Review, ShippingAddress } from "@acme/shop-db";

// A product as the order pages see it: a line of an order, a review and a
// product's page refer to it by its id, and name it by its name.
@source(Product)
@display({ noun: "Product", plural: "Products", titleField: "name", summaryFields: ["name", "sku", "priceCents", "inStock"] })
export abstract class ProductView {
  id: Identity.UUID;
  sku: Identity.Slug;
  name: Identity.Name;
  priceCents: Generic.Int64;
  inStock: boolean;
}

// One line of an order as a caller sees it.
@source(OrderLine)
export abstract class OrderLineView {
  id: Identity.UUID;
  quantity: Generic.Int64;
  unitPriceCents: Generic.Int64;

  // The product this line is for.
  @virtual
  productId: RefersTo<Identity.UUID, ProductView>;
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
  productId: RefersTo<Identity.UUID, ProductView>;
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

// Reading orders: a shopper reads their own, and staff, who hold
// orders.any, every customer's.
export class OrderQueries extends Authenticated {
  // One order: the caller's own, or any customer's for staff. Another
  // customer's order is not found, as one no one placed is, so its id
  // tells a shopper nothing.
  @rest(HttpMethod.GET, "orders/{id}")
  @requirePermission(["orders.read"])
  @requireOwnership
  getOrder(id: Identity.UUID): OrderView {
    throw new Error("schema declaration only");
  }

  // The caller's orders, or every customer's for staff, oldest first: by
  // when each was placed, then by its id.
  @rest(HttpMethod.GET, "orders")
  @requirePermission(["orders.read"])
  @paginated({ defaultLimit: 20, maxLimit: 100 })
  listOrders(statuses: QueryParam<Nullable<OrderStatus[]>>): OrderView[] {
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

  // Cancels an order that has not shipped, recording why: the caller's
  // own, or any customer's for staff. Another customer's order is not
  // found, as getOrder answers.
  @rest(HttpMethod.POST, "orders/{id}/cancel")
  @requirePermission(["orders.write"])
  @requireOwnership
  cancelOrder(id: Identity.UUID, reason: Nullable<Validate<string, { maxLength: 500 }>>): OrderView {
    throw new Error("schema declaration only");
  }
}

// The catalog a shopper orders from: anyone may read it. shop-api keeps
// the staff's catalog, which adds and edits the products.
export class ProductCatalog {
  // The products, by name, then by id; ids narrows them to the products
  // with those ids, the lookup a page names its products' ids through.
  @rest(HttpMethod.GET, "products")
  @publicRoute
  @paginated({ defaultLimit: 20, maxLimit: 100 })
  listProducts(
    ids: QueryParam<Nullable<Validate<RefersTo<Identity.UUID, ProductView>[], { listMax: 100 }>>>
  ): ProductView[] {
    throw new Error("schema declaration only");
  }

  // One product. Its parameter is productId, as the reviews' routes name
  // the parameter at that place.
  @rest(HttpMethod.GET, "products/{productId}")
  @publicRoute
  getProduct(productId: RefersTo<Identity.UUID, ProductView>): ProductView {
    throw new Error("schema declaration only");
  }
}

// A product's reviews: anyone may read them, and a signed-in shopper may
// write one.
export class ProductReviews {
  // The product's reviews, oldest first: by when each was written, then by
  // its id. q keeps the reviews whose title or body holds each of its
  // words.
  @rest(HttpMethod.GET, "products/{productId}/reviews")
  @publicRoute
  @paginated({ defaultLimit: 20, maxLimit: 50 })
  listReviews(
    productId: RefersTo<Identity.UUID, ProductView>,
    minRating: QueryParam<Nullable<number>>,
    q: SearchQuery<Validate<string, { maxLength: 100 }>>
  ): ReviewView[] {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.POST, "products/{productId}/reviews")
  @auth
  writeReview(productId: RefersTo<Identity.UUID, ProductView>, input: WriteReviewInput): ReviewView {
    throw new Error("schema declaration only");
  }
}
