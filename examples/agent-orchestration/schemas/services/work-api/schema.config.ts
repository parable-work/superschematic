import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { WorkDb } from "@schemas/work-db";

// The work app's API, served from work-db's tables in TypeScript. Its
// agents run in the runtime-providers example's local-process sandbox,
// their turns completed by its model stub, their credentials resolved by
// its vault broker, and their approvals asked for by its SMS relay.
export default defineConfig({
  name: "work-api",
  kind: SchemaKind.API,
  dependencies: [WorkDb],
  providers: [
    "../../../../runtime-providers/providers/local-sandbox",
    "../../../../runtime-providers/providers/stub-model",
    "../../../../runtime-providers/providers/vault-broker",
    "../../../../runtime-providers/providers/sms-relay-a"
  ],
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    api: { enabled: true, language: "TYPESCRIPT" }
  }
});
