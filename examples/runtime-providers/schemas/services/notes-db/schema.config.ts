import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The notes app's tables: notes, and the notifier's tables, whose notices
// a channel adapter delivers (D153, D159). The notifier runs on Postgres,
// in TypeScript: the Go ORM serves no notifier yet (D180).
export default defineConfig({
  name: "notes-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { dialects: ["postgres"] }
  }
});
