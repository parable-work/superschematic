import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "fixture-recurrence-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true }
    },
    sql: { dialects: ["postgres", "sqlite"] }
  }
});
