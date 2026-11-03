import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "fixture-app-api",
  kind: SchemaKind.API,
  outputs: {
    types: {
      [TargetLanguage.Rust]: { enabled: true }
    },
    api: { enabled: true, language: "RUST" }
  }
});
