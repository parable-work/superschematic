import { defineConfig, SchemaKind, service, TargetLanguage } from "@superschematic/schema-config";

// A public API schema. authDb names the DB schema the auth provider probes
// for its stores: the acme "apikey" provider finds shop-db's ApiKey and User
// tables and generates the key and principal store adapters over its ORM.
export default defineConfig({
  name: "shop-api",
  kind: SchemaKind.API,
  public: true,
  authDb: service({ name: "shop-db", kind: SchemaKind.DB }),
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Go]: { enabled: true }
    },
    api: { enabled: true },
    sdk: {
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
