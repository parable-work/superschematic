import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// Mergeable tables under per-record access (D147, D156, D180, D181):
// labels with an audience, the notes and boards that reference them, and
// brands whose name is Aliased, so a merge keeps to the rows its caller may
// write and Aliased's resolve and its refusals to the rows its caller may
// read. The Go, TypeScript and Rust outputs, on Postgres.
export default defineConfig({
  name: "fixture-merge-access-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    },
    sql: { dialects: ["postgres"] }
  }
});
