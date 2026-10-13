import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "notes-db",
  kind: SchemaKind.DB,
  outputs: {
    types: { [TargetLanguage.Go]: { enabled: true } }
  }
});
