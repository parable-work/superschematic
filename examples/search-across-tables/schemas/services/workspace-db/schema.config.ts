import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The tables of a team's workspace, every one found by one search: projects,
// tasks with a conversation and files, and pages a person may keep to
// themselves.
export default defineConfig({
  name: "workspace-db",
  kind: SchemaKind.DB,
  outputs: {
    // Go types too, as every DB service has: the DB kind always generates
    // the Go ORM, which imports them.
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    // The keys of Task's Encrypted<T> access code (D85): the secret an API on
    // workspace-db declares in its @envVars. Search never opens the field, so
    // the server opens the ORM without them.
    sql: { dialects: ["postgres"], encryption: { key: "FieldKeys.WORKSPACE_DB_FIELD_KEYS" } }
  }
});
