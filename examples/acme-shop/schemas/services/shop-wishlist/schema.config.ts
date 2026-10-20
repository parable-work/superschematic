import { ShopWishlistDb } from "@acme/shop-wishlist-db";
import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The wishlist, a TypeScript API over shop-wishlist-db, its one DB-kind
// dependency, which lists sqlite first: its Deps.db is a SqlDatabase, D1's
// interface, on Bun and on Cloudflare Workers alike (D64).
export default defineConfig({
  name: "shop-wishlist",
  kind: SchemaKind.API,
  dependencies: [ShopWishlistDb],
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    api: { enabled: true, language: "TYPESCRIPT" }
  }
});
