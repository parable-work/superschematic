import { ShopDb } from "@acme/shop-db";
import { ShopMedia } from "@acme/shop-media";
import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "shop-api",
  kind: SchemaKind.API,
  public: true,
  authDb: ShopDb,
  // Product images (D54): the implementation's Deps holds the bucket.
  buckets: [ShopMedia],
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    api: { enabled: true },
    sdk: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
