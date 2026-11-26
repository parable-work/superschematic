import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The tables of per-record access (D156) that fixture-access-api serves:
// folders and documents with an audience, contracts whose requests a row
// rule reaches through relations, the grants that let a party reach rows,
// and the access log. Built for Postgres and SQLite, whose TypeScript
// repositories serve both (D182).
export default defineConfig({
  name: "fixture-access-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { dialects: ["postgres", "sqlite"] }
  }
});
