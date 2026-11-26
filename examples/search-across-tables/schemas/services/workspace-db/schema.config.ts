import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The tables of a team's workspace, every one found by one search: projects,
// tasks with a conversation and files, and pages a person may keep to
// themselves.
export default defineConfig({
  name: "workspace-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { dialects: ["postgres"] }
  }
});
