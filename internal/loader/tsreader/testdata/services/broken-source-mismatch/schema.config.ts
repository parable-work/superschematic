import { defineConfig, SchemaKind, service, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "broken-source-mismatch",
  kind: SchemaKind.API,
  dependencies: [service({ name: "fixture-db", kind: SchemaKind.DB })],
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
