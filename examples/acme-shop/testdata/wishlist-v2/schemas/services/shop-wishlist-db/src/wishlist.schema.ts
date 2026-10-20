import { Generic, Identity, Temporal } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, index, key } from "@superschematic/db";

// The wishlist's second version: a shopper keeps a quantity of each
// product, and the free-text note goes. The plan from the first version
// (schemas/services/shop-wishlist-db) adds quantity in expand, nullable,
// so the first version's servers, which never write it, keep inserting
// while the rollout runs, and drops note in contract, once none of them
// runs: a destructive hazard the deploy stops on until --allow names it.
@index<WishlistItem>(["shopper", "sku"], { unique: true, name: "one_per_shopper_and_sku" })
export abstract class WishlistItem {
  @key
  id: AutoGenerate<Identity.UUID>;

  shopper: Identity.Slug;

  sku: Identity.Slug;

  // How many the shopper wants; null on a row the first version kept,
  // which counts one.
  quantity: Nullable<Generic.Int64>;

  addedAt: AutoGenerate<Temporal.DateTime>;
}
