import { defineConfig, SchemaKind, service, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "fixture-api",
  kind: SchemaKind.API,
  public: true,
  // authDb is auth linkage only — not a codegen dependency for @source.
  authDb: service({ name: "fixture-db", kind: SchemaKind.DB }),
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    api: { enabled: true },
    sdk: {
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
