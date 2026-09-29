import { defineConfig, SchemaKind, service, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "shop-api",
  kind: SchemaKind.API,
  public: true,
  authDb: service({ name: "shop-db", kind: SchemaKind.DB }),
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
