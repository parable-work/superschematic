import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "fixture-recurrence-db",
  kind: SchemaKind.DB,
  // No Go types: the Go ORM serves neither Recurrence nor a calendar
  // source yet (D180).
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    },
    sql: { dialects: ["postgres", "sqlite"] }
  }
});
