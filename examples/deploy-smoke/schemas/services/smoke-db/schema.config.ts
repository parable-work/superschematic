import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The smoke's database: the user model, line items with a vector column,
// a keyed queue a worker drains, and a declared query. It lists postgres
// alone, so the cloudflare target places it on Hyperdrive (D92).
export default defineConfig({
  name: "smoke-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
