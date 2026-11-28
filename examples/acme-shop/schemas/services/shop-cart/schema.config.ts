import { ShopDb } from "@acme/shop-db";
import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// A shopper's cart and the shop's pickup slots, served in TypeScript over
// shop-db's tables (D70, D103): a cart is a Checkout row while it is in
// cart, which shop-orders then starts, and the slots are what each
// CollectionPoint's Calendar has free. shop-db is its authDb, so the
// sessions shop-api's sign-in starts authenticate its callers. Its
// implementation reads and writes checkouts, tickets, a shopper's own
// reviews and their reorders through Deps.orm, shop-db's generated
// TypeScript repositories (D79), which run the tables' behaviors through
// the behaviors' core.
export default defineConfig({
  name: "shop-cart",
  kind: SchemaKind.API,
  authDb: ShopDb,
  dependencies: [ShopDb],
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    api: { enabled: true, language: "TYPESCRIPT" },
    sdk: {
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
