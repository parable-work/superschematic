import { defineConfig, SchemaKind, service, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "shop-storefront",
  kind: SchemaKind.API,
  dependencies: [service({ name: "shop-common", kind: SchemaKind.General })],
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    api: { enabled: true, language: "TYPESCRIPT" },
    sdk: {
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
