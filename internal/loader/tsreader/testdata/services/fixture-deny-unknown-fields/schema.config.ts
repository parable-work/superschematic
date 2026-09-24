import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "fixture-deny-unknown-fields",
  kind: SchemaKind.General,
  outputs: {
    types: {
      [TargetLanguage.Rust]: { enabled: true }
    }
  }
});
