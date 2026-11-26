import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// Events, notifications and channels (D153): tickets that raise events,
// some of them private to their owner; the notifier's tables, its dispatch
// queue and the channel identities a reply resolves through; and the
// queue a referenced row's delete enqueues on.
export default defineConfig({
  name: "fixture-notifications-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { dialects: ["postgres"] }
  }
});
