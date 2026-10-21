import { Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, index, key } from "@superschematic/db";

// A product a shopper keeps for later, with a note of their own: one row
// per shopper and SKU. The shopper is the handle the storefront knows them
// by, since the wishlist reads no user model.
@index<WishlistItem>(["shopper", "sku"], { unique: true, name: "one_per_shopper_and_sku" })
export abstract class WishlistItem {
  @key
  id: AutoGenerate<Identity.UUID>;

  shopper: Identity.Slug;

  sku: Identity.Slug;

  note: Nullable<string>;

  addedAt: AutoGenerate<Temporal.DateTime>;
}
