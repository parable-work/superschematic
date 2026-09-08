import { defineConfig, SchemaKind, TargetLanguage } from "@psgen/schema-config";

export default defineConfig({
  name: "fixture-traits",
  kind: SchemaKind.General,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
