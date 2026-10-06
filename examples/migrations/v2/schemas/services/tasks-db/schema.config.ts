import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "tasks-db",
  kind: SchemaKind.DB,
  outputs: {
    types: { [TargetLanguage.Go]: { enabled: true } },
    // Built for Postgres and SQLite: each gets a create.sql, and migrate
    // plan --dialect plans either.
    sql: { dialects: ["postgres", "sqlite"] }
  }
});
