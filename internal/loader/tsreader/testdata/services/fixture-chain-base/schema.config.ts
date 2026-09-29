import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The far end of the fixture-chain services: base <- common <- db <- api,
// each importing a type from the one before.
export default defineConfig({
  name: "fixture-chain-base",
  kind: SchemaKind.General,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true }
    }
  }
});
