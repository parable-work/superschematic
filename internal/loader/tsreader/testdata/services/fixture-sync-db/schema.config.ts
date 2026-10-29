import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// A bank sync's tables (D89): line items a sync upserts by the bank's id
// for each, in batches, only when they changed, and a trigger that enqueues
// a message on every write of one.
export default defineConfig({
  name: "fixture-sync-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Go]: { enabled: true }
    }
  }
});
