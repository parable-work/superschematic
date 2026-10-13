import { ShopDb } from "@acme/shop-db";
import { ShopStock } from "@acme/shop-stock";
import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "shop-orders",
  kind: SchemaKind.API,
  public: true,
  authDb: ShopDb,
  dependencies: [ShopDb],
  // Placing an order reserves its stock on shop-stock, a Rust server, through
  // its Go SDK (D57): the implementation's Deps holds the client.
  calls: [ShopStock],
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    },
    api: { enabled: true },
    sdk: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    }
  }
});
