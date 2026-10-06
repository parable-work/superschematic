import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "fixture-service-caller-api",
  kind: SchemaKind.API,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true }
    },
    api: { enabled: true }
  }
});
