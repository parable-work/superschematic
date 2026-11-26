import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { DutiesDb } from "@schemas/duties-db";

// The duties app's API, served from duties-db's tables in TypeScript.
export default defineConfig({
  name: "duties-api",
  kind: SchemaKind.API,
  dependencies: [DutiesDb],
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    api: { enabled: true, language: "TYPESCRIPT" }
  }
});
