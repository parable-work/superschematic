import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureAudienceQueueDb } from "@schemas/fixture-audience-queue-db";

// An API over fixture-audience-queue-db whose worker of the pages' queue
// the generated wiring hands each message to the runtime's resolving of an
// audience (D156, #734), with no method of the implementation's.
export default defineConfig({
  name: "fixture-audience-queue-api",
  kind: SchemaKind.API,
  dependencies: [FixtureAudienceQueueDb],
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true }
    },
    api: { enabled: true }
  }
});
