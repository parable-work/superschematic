import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The tables of an issue tracker whose rows point at one another and hold
// files: issues with a conversation, and notes a person may keep to
// themselves.
export default defineConfig({
  name: "issues-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { dialects: ["postgres"] }
  }
});
