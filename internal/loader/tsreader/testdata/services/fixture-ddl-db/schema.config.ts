import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// What the DDL says beyond a table's columns (D78): defaults, checks, a
// relation that SET NULL clears, richer indexes and raw SQL. Built for
// SQLite too, which runs the raw SQL that names it.
export default defineConfig({
  name: "fixture-ddl-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Go]: { enabled: true }
    },
    sql: { dialects: ["postgres", "sqlite"] }
  }
});
