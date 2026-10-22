import { ShopDb } from "@acme/shop-db";
import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The warehouse's stock, served in Rust (D57) over shop-db's Inventory,
// which the engine keeps (D70): its implementation calls the engine
// in-process. shop-db is its authDb, so the sessions shop-api's sign-in
// starts authenticate its callers too, and its database, whose queue
// StockLow its Rust worker ReorderStock handles (D65). shop-orders' Go
// server and job call it through the Go SDK when a checkout is placed. A
// Rust server holds no bucket.
export default defineConfig({
  name: "shop-stock",
  kind: SchemaKind.API,
  authDb: ShopDb,
  dependencies: [ShopDb],
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    },
    api: { enabled: true, language: "RUST" },
    sdk: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    }
  }
});
