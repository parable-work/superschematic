import { ShopDb } from "@schemas/shop-db";
import { ShopMedia } from "@schemas/shop-media";
import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "shop-api",
  kind: SchemaKind.API,
  authDb: ShopDb,
  // Its product images (D54).
  buckets: [ShopMedia],
  outputs: {
    types: { [TargetLanguage.Go]: { enabled: true } },
    api: { enabled: true, language: "GO" },
    // shop-orders calls shop-api, so its Go server's Deps holds this SDK.
    sdk: { [TargetLanguage.Go]: { enabled: true } }
  }
});
