import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "fixture-maps",
  kind: SchemaKind.General,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    }
  }
});
