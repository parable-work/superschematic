import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The notes app's tables: notes, and the notices sent about them, which a
// channel adapter delivers (D159).
export default defineConfig({
  name: "notes-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
