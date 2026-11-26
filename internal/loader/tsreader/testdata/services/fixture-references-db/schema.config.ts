import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// References and attachments on any row (D154): tickets with a
// conversation, references to rows of any table and files in a bucket;
// notes that point at tickets and other notes and hold an audience; the
// queues a target's delete and a file's release enqueue on. Built for
// Postgres and SQLite, whose TypeScript repositories serve both (D182).
export default defineConfig({
  name: "fixture-references-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { dialects: ["postgres", "sqlite"] }
  }
});
