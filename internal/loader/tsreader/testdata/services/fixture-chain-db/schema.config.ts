import { defineConfig, SchemaKind, service, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "fixture-chain-db",
  kind: SchemaKind.DB,
  dependencies: [service({ name: "fixture-chain-common", kind: SchemaKind.General })],
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true }
    }
  }
});
