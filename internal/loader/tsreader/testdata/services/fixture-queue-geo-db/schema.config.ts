import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// A queue whose message holds a location and a duration, which Postgres
// stores as POINT and INTERVAL columns. SQLite has no storage for a POINT,
// so this database is Postgres alone, apart from fixture-queue-db, which
// writes its queues in both dialects.
export default defineConfig({
  name: "fixture-queue-geo-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Go]: { enabled: true }
    },
    sql: { dialects: ["postgres"] }
  }
});
