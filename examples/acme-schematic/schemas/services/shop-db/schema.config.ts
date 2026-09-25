import { defineConfig, SchemaKind, TargetLanguage } from "@acme/schema-config";

// A DB schema: the core sql and orm generators run for it, and types in the
// languages listed here. The acme manifest generator (ext/manifest.go) is
// appended to the pipeline because it lists DB among its kinds.
export default defineConfig({
  name: "shop-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Go]: { enabled: true }
    }
  }
});
