import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureQueueDb } from "@schemas/fixture-queue-db";

export default defineConfig({
  name: "fixture-queue-api",
  kind: SchemaKind.API,
  authDb: FixtureQueueDb,
  dependencies: [FixtureQueueDb],
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true }
    },
    api: { enabled: true }
  }
});
