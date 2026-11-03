import { Generic, Identity, Temporal } from "superscalar";
import { Validate } from "@superschematic/schema";
import { Authenticated, HttpMethod, SearchQuery, SortBy, paginated, requirePermission, rest } from "@superschematic/api";

// A review the caller wrote, as their own list shows it. shop-cart keeps a
// shopper's reviews and reorders in shop-db's tables through the database's
// generated TypeScript repositories (D79), with no SQL of its own.
export abstract class MyReview {
  id: Identity.UUID;
  productId: Identity.UUID;
  rating: number;
  title: string;
  body: string;
  createdAt: Temporal.DateTime;
}

export abstract class WriteMyReviewInput {
  rating: Validate<number, { min: 1; max: 5 }>;
  title: Validate<string, { minLength: 1; maxLength: 120 }>;
  body: Validate<string, { maxLength: 5000 }>;
}

// The order a reorder placed, at the products' prices now.
export abstract class ReorderView {
  orderId: Identity.UUID;
  totalCents: Generic.Int64;
  lineCount: Generic.Int64;
}

// The caller's own reviews: each shopper reviews a product once, so writing
// one again changes it, and deleting one hides it, the row kept, marked
// deleted.
export class MyReviews extends Authenticated {
  // The caller's reviews, oldest first: by when each was written, or by its
  // rating with sort, then by its id. q keeps those whose title or body
  // holds each of its words.
  @rest(HttpMethod.GET, "my/reviews")
  @requirePermission(["orders.read"])
  @paginated({ defaultLimit: 20, maxLimit: 50, defaultSort: "createdAt" })
  listMyReviews(q: SearchQuery<Validate<string, { maxLength: 100 }>>, sort: SortBy<MyReview, ["createdAt", "rating"]>): MyReview[] {
    throw new Error("schema declaration only");
  }

  // Writes the caller's review of the product, or changes the one they
  // wrote.
  @rest(HttpMethod.PUT, "my/reviews/{productId}")
  @requirePermission(["orders.write"])
  writeMyReview(productId: Identity.UUID, input: WriteMyReviewInput): MyReview {
    throw new Error("schema declaration only");
  }

  // Deletes one of the caller's reviews; another's is not found.
  @rest(HttpMethod.DELETE, "my/reviews/{id}")
  @requirePermission(["orders.write"])
  deleteMyReview(id: Identity.UUID): MyReview {
    throw new Error("schema declaration only");
  }
}

// Places one of the caller's orders again.
export class Reorders extends Authenticated {
  // A new order of the same products and quantities, at today's prices,
  // shipped to the same address, written in one transaction with its lines
  // and the OrderPlaced message shop-orders' worker fulfils it from.
  @rest(HttpMethod.POST, "orders/{id}/reorder")
  @requirePermission(["orders.write"])
  reorder(id: Identity.UUID): ReorderView {
    throw new Error("schema declaration only");
  }
}
