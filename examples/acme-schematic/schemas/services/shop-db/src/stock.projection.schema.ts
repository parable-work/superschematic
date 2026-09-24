import { Identity } from "superscalar";
import { column, join, projection } from "@superschematic/db";

import { Product, StockLevel } from "./shop.schema";

// What the current shop has on its shelves: one row per product it still
// carries. The first rule binds the shop scope, which acme's projection
// policy (ext/projection_policy.go) requires of every view.
@projection<StockLevel>({
  pool: "storefront",
  name: "stock",
  migration: "20260923120000",
  where: [
    { column: "base.shop", setting: "acme.shop_id" },
    { column: "base.discontinuedAt", isNull: true }
  ]
})
@join<Product>("product", { "product.id": "base.product" })
export abstract class ShopStock {
  @column("product.sku")
  sku: Identity.Slug;

  @column("product.name")
  name: Identity.Name;

  quantity: number;

  @column("product.priceCents")
  priceCents: number;

  @column("product.inStock")
  inStock: boolean;
}
