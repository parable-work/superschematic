import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// A folder with very many pages, whose pages inherit its audience through
// a queue (D156, #734): a folder's change of audience enqueues it, and the
// queue's worker resolves its pages again after the commit, a batch at a
// time. The Go, TypeScript and Rust outputs, on Postgres.
export default defineConfig({
  name: "fixture-audience-queue-db",
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
