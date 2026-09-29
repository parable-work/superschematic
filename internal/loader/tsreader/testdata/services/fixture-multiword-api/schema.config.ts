import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// A Rust REST API whose operation sets have two-word names, so the generated
// namespace is kebab-case (`pool-search`).
export default defineConfig({
  name: "fixture-multiword-api",
  kind: SchemaKind.API,
  outputs: {
    types: {
      [TargetLanguage.Rust]: { enabled: true }
    },
    api: { enabled: true, language: "RUST" }
  }
});
