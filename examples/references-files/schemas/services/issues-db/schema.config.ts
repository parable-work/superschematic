import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The tables of an issue tracker whose rows point at one another and hold
// files: issues with a conversation, and notes a person may keep to
// themselves.
export default defineConfig({
  name: "issues-db",
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
