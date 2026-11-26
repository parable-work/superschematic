import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The tables of a records app with per-record access: workspaces and pages
// with an audience, agreements whose tickets a row rule reaches through
// relations, the grants that let a party reach rows, and the access log.
export default defineConfig({
  name: "records-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { dialects: ["postgres"] }
  }
});
