import { defineConfig, SchemaKind, TargetLanguage } from "@psgen/schema-config";

export default defineConfig({
  name: "fixture-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Go]: { enabled: true }
    }
  }
});
