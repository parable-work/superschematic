import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "fixture-nested-arrays",
  kind: SchemaKind.General,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    }
  }
});
