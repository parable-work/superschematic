import { ShopApi } from "@schemas/shop-api";
import { ShopDb } from "@schemas/shop-db";
import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "shop-orders",
  kind: SchemaKind.API,
  authDb: ShopDb,
  dependencies: [ShopDb],
  calls: [ShopApi],
  outputs: {
    types: { [TargetLanguage.Go]: { enabled: true } },
    api: { enabled: true }
  }
});
