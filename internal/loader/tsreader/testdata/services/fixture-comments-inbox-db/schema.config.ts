import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// A table's conversation in the notifier's inbox (#713): tickets, some of
// them private to their owner, whose Comments emits its mentions and its
// replies for the notifier, and the notifier's tables and dispatch queue.
export default defineConfig({
  name: "fixture-comments-inbox-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { dialects: ["postgres"] }
  }
});
