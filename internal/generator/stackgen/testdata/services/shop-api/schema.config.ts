import { ShopDb } from "@schemas/shop-db";
import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "shop-api",
  kind: SchemaKind.API,
  authDb: ShopDb,
  outputs: {
    types: { [TargetLanguage.Go]: { enabled: true } },
    api: { enabled: true, language: "GO" },
    // shop-orders calls shop-api, so its Go server's Deps holds this SDK.
    sdk: { [TargetLanguage.Go]: { enabled: true } }
  }
});
