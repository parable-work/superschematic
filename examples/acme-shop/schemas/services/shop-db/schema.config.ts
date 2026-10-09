import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "shop-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    },
    // SQLite too: the Topcoat app in topcoat/ can keep the shop in a
    // SQLite file, migrated from the SQLite plan.
    sql: { dialects: ["postgres", "sqlite"] }
  }
});
