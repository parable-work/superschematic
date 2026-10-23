import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureDerivedDb } from "@schemas/fixture-derived-db";

export default defineConfig({
  name: "fixture-derived-api",
  kind: SchemaKind.API,
  authDb: FixtureDerivedDb,
  dependencies: [FixtureDerivedDb],
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    api: { enabled: true, language: "TYPESCRIPT" }
  }
});
