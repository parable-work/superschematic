import { ShopDb } from "@schemas/shop-db";
import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "shop-api",
  kind: SchemaKind.API,
  authDb: ShopDb,
  outputs: {
    types: { [TargetLanguage.Go]: { enabled: true } },
    api: { enabled: true, language: "GO" }
  }
});
