import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The memory store shop-api lists and counts its rate limits in (D75): a
// Valkey server every replica of shop-api shares, and can lose. It holds no
// fact: a channel wakes a waiter, which reads the product in shop-db, and a
// claim stops a piece of work running twice at once. Its build writes the
// Go definitions shop-api's implementation names them with.
export default defineConfig({
  name: "shop-cache",
  kind: SchemaKind.MemoryStore,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true }
    }
  }
});
