import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureBaseLib } from "@schemas/fixture-base-lib";

export default defineConfig({
  name: "fixture-foreign-base",
  kind: SchemaKind.General,
  dependencies: [FixtureBaseLib],
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true }
    }
  }
});
