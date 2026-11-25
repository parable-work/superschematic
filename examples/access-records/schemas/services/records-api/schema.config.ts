import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { RecordsDb } from "@schemas/records-db";

// The records app's API, served from records-db's tables in TypeScript.
export default defineConfig({
  name: "records-api",
  kind: SchemaKind.API,
  dependencies: [RecordsDb],
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    api: { enabled: true }
  }
});
