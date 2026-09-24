import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// An API schema the acme tests load and generate: it declares acme's
// confirm key on @mcp. The example's own schemas do not, because the
// core-only binary builds shop-api and would reject the key.
export default defineConfig({
  name: "returns-api",
  kind: SchemaKind.API,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    api: { enabled: true },
    sdk: {
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
