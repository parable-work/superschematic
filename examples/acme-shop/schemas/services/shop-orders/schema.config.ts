import { ShopDb } from "@acme/shop-db";
import { ShopStock } from "@acme/shop-stock";
import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "shop-orders",
  kind: SchemaKind.API,
  public: true,
  authDb: ShopDb,
  dependencies: [ShopDb],
  // A checkout placed tells shop-stock, a Rust server, what it sold,
  // through its Go SDK (D57), and shop-stock files a restock request for a
  // product it ran low: the implementation's Deps holds the client.
  calls: [ShopStock],
  // The server turns away a client too old to speak to it (D143): its
  // EnvConfig reads MIN_CLIENT_VERSIONS, such as "cli=0.5.0", and a
  // request whose X-Client-Version names an older cli is answered 426.
  clientVersions: true,
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
