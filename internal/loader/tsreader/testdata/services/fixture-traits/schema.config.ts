import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "fixture-traits",
  kind: SchemaKind.General,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
