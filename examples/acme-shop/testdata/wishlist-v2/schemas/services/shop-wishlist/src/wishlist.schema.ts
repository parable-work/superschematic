import { Generic, Identity, Temporal } from "superscalar";
import { Validate } from "@superschematic/schema";
import { HttpMethod, publicRoute, rest } from "@superschematic/api";

// A product a shopper keeps, and how many of it they want.
export abstract class WishlistEntry {
  sku: Identity.Slug;
  quantity: Generic.Int64;
  addedAt: Temporal.DateTime;
}

// A shopper's wishlist, the products they keep, oldest first.
export abstract class WishlistView {
  shopper: Identity.Slug;
  items: WishlistEntry[];
}

// What a shopper keeps a product with: one when they say no quantity.
export abstract class KeepInput {
  quantity?: Validate<Generic.Int64, { min: 1; max: 99 }>;
}

export class WishlistQueries {
  @rest(HttpMethod.GET, "wishlists/{shopper}")
  @publicRoute
  getWishlist(shopper: Identity.Slug): WishlistView {
    throw new Error("schema declaration only");
  }
}

export class WishlistMutations {
  // Keeps the product, or changes its quantity when the shopper keeps it
  // already.
  @rest(HttpMethod.PUT, "wishlists/{shopper}/items/{sku}")
  @publicRoute
  keepItem(shopper: Identity.Slug, sku: Identity.Slug, input: KeepInput): WishlistView {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.DELETE, "wishlists/{shopper}/items/{sku}")
  @publicRoute
  dropItem(shopper: Identity.Slug, sku: Identity.Slug): WishlistView {
    throw new Error("schema declaration only");
  }
}
