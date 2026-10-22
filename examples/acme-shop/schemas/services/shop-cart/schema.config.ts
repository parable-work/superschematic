import { ShopDb } from "@acme/shop-db";
import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// A shopper's cart and the shop's pickup slots, served in TypeScript over
// shop-db's engine (D70): a cart is the engine's Checkout while it is in
// cart, which shop-orders then starts, and the slots are what each
// CollectionPoint's Calendar has free. Its implementation calls the engine
// in-process. shop-db is its authDb, so the sessions shop-api's sign-in
// starts authenticate its callers. shop-db's engine is a native addon, so
// it runs on a server that builds an image, never as a Worker, which is
// why the cart lives here and not in shop-storefront.
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
