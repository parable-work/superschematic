import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "shop-wishlist-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    // SQLite first (D64): shop-wishlist's Deps.db is a SqlDatabase, D1's
    // interface, which a SQLite file gives it under stack dev and a D1
    // binding on Cloudflare Workers. Postgres stays listed, as every DB
    // service's dialects must.
    sql: { dialects: ["sqlite", "postgres"] }
  }
});
