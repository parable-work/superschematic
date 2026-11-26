import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The tables of a duties app (D152): duties that recur, the holidays their
// due dates move off, the agreements a duty may count back from, and the
// users, roles and grants a calendar feed reads through.
export default defineConfig({
  name: "duties-db",
  kind: SchemaKind.DB,
  outputs: {
    // Go types too, as every DB service has: the DB kind always generates
    // the Go ORM, which imports them.
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { dialects: ["postgres"] }
  }
});
