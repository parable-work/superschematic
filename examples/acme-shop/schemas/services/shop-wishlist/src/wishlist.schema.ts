import { Identity, Temporal } from "superscalar";
import { Nullable, Validate } from "@superschematic/schema";
import { HttpMethod, publicRoute, rest } from "@superschematic/api";

// A product a shopper keeps, with their note.
export abstract class WishlistEntry {
  sku: Identity.Slug;
  note: Nullable<string>;
  addedAt: Temporal.DateTime;
}

// A shopper's wishlist, the products they keep, oldest first.
export abstract class WishlistView {
  shopper: Identity.Slug;
  items: WishlistEntry[];
}

// What a shopper keeps a product with.
export abstract class KeepInput {
  note?: Validate<string, { maxLength: 280 }>;
}

export class WishlistQueries {
  @rest(HttpMethod.GET, "wishlists/{shopper}")
  @publicRoute
  getWishlist(shopper: Identity.Slug): WishlistView {
    throw new Error("schema declaration only");
  }
}

export class WishlistMutations {
  // Keeps the product, or changes its note when the shopper keeps it already.
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
