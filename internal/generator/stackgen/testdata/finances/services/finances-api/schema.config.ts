import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FinancesDb } from "@schemas/finances-db";

export default defineConfig({
  name: "finances-api",
  kind: SchemaKind.API,
  authDb: FinancesDb,
  outputs: {
    types: { [TargetLanguage.TypeScript]: { enabled: true } },
    api: { enabled: true, language: "TYPESCRIPT" }
  }
});
