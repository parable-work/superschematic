import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The tickets app's tables (D153): tickets and duties that raise events,
// the notifier's tables and the channel identities replies come from. The
// notifier runs on Postgres.
export default defineConfig({
  name: "tickets-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { dialects: ["postgres"] }
  }
});
