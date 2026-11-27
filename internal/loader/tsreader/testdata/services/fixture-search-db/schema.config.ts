import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// Search across tables (D155): six @searchable tables, two of them with an
// audience, one with a conversation and files, one soft-deleting, one with
// an encrypted field that stays out of the index; the grants a party
// reaches rows through; and the queue a write that changes the indexed
// text enqueues on. Built for Postgres and SQLite, whose TypeScript
// runtime searches both (D186).
export default defineConfig({
  name: "fixture-search-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { dialects: ["postgres", "sqlite"] }
  }
});
