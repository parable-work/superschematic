import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// Seed rows (#625): tables a database holds rows of from its first
// migration on, on Postgres and SQLite.
export default defineConfig({
  name: "fixture-seed-rows-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { dialects: ["postgres", "sqlite"] }
  }
});
