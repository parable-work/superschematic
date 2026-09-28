import { defineConfig, SchemaKind, service, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "fixture-chain-common",
  kind: SchemaKind.General,
  dependencies: [service({ name: "fixture-chain-base", kind: SchemaKind.General })],
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true }
    }
  }
});
