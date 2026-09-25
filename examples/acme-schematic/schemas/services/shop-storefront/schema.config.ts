import { defineConfig, SchemaKind, TargetLanguage } from "@acme/schema-config";

// An API schema served by the TypeScript server: the api generator emits a
// Hono router package (@acme/shop-storefront-api) instead of the Go module.
// The router is provider-neutral. Every route's auth requirement is in its
// operation table, and the storefront app (examples/acme-schematic/storefront)
// supplies the authenticator that resolves the caller's X-API-Key.
export default defineConfig({
  name: "shop-storefront",
  kind: SchemaKind.API,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    api: { enabled: true, language: "TYPESCRIPT" }
  }
});
