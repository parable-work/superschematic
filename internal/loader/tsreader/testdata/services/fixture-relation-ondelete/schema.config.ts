import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "fixture-relation-ondelete",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Go]: { enabled: true }
    }
  }
});
