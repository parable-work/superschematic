import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "fixture-strict-json",
  kind: SchemaKind.General,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    }
  }
});
