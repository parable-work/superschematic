import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The work app's tables (D158): tasks agents work on, the grants a run's
// token is, the notifier that asks for approvals, and the orchestration's
// own tables. The orchestration runs on Postgres.
export default defineConfig({
  name: "work-db",
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
