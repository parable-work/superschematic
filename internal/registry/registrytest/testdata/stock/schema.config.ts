import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The TypeScript twin of stock-json: a General schema whose type composes
// the fixture extension's behaviors with @behavior.
export default defineConfig({
  name: "stock",
  kind: SchemaKind.General,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
