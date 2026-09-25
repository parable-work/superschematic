import { defineConfig, SchemaKind, TargetLanguage } from "@acme/schema-config";

// A General schema: plain types and an @envVars contract, no tables and no
// routes. Types come out in TypeScript and Python; the core envConfig
// generator writes a Go loader for the ShopConfig class.
export default defineConfig({
  name: "shop-config",
  kind: SchemaKind.General,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true }
    }
  }
});
