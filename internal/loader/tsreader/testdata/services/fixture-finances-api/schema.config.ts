import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureFinancesDb } from "@schemas/fixture-finances-db";

export default defineConfig({
  name: "fixture-finances-api",
  kind: SchemaKind.API,
  dependencies: [FixtureFinancesDb],
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Go]: { enabled: true }
    },
    api: { enabled: true }
  }
});
