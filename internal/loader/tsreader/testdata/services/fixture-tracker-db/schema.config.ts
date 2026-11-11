import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The tables of an issue tracker, which fixture-tracker-api serves through
// @tableOperation (D106): a table whose rows a caller creates under a
// parent, an audited table that is soft-deleted and searched, and one
// that is hard-deleted.
export default defineConfig({
  name: "fixture-tracker-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    },
    sql: { dialects: ["postgres"] }
  }
});
