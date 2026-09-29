import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// An @envVars class with TypeScript output: envgen's TypeScript loader tests
// generate its types package and config.ts and load it against env maps.
export default defineConfig({
  name: "fixture-env",
  kind: SchemaKind.General,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
