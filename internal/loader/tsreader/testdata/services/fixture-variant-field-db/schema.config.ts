import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// A table with a discriminated-union field (D149): a step's result takes
// the shape its kind names.
export default defineConfig({
  name: "fixture-variant-field-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    },
    sql: { dialects: ["postgres", "sqlite"] }
  }
});
