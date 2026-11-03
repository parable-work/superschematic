import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// Statements a caller imports as files and exports as CSV (D81): an input
// with a file part beside its JSON fields, and results that are files.
export default defineConfig({
  name: "fixture-files-api",
  kind: SchemaKind.API,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    api: { enabled: true, language: "TYPESCRIPT" },
    sdk: {
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
