import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The deliveries a runtime provider records (D159, D185): each notice a
// channel adapter sent, which fixture-providers-api serves.
export default defineConfig({
  name: "fixture-providers-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { dialects: ["postgres"] }
  }
});
