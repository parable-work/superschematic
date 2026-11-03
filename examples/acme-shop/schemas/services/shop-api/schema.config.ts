import { ShopCache } from "@acme/shop-cache";
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
  // shop-cache (D75): every replica's Deps holds the memory store, which
  // wakes a request waiting for a product and stops an image upload being
  // signed twice at once, and the @rateLimit routes count in it, so the
  // replicas share one budget.
  memoryStores: [ShopCache],
  rateLimitStore: ShopCache,
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
