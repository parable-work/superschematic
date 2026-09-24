import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "fixture-base-lib",
  kind: SchemaKind.General,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true }
    }
  }
});
