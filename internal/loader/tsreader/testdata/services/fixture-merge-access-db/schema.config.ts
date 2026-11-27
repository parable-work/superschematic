import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// A Mergeable table under per-record access (D147, D156, D180): labels
// with an audience, and the notes that reference them, so a merge keeps to
// the rows its caller may write. The Go outputs, on Postgres.
export default defineConfig({
  name: "fixture-merge-access-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true }
    },
    sql: { dialects: ["postgres"] }
  }
});
