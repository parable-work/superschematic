import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureCacheCommon } from "@schemas/fixture-cache-common";

export default defineConfig({
  name: "fixture-cache",
  kind: SchemaKind.MemoryStore,
  dependencies: [FixtureCacheCommon],
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true }
    }
  }
});
