import { defineConfig } from "@superschematic/schema-config";

// A schema of the Catalog kind, which only the acme extension registers.
// SchemaKind is closed to the core three; an extension kind is written as a
// string (SchemaKindName admits any registered name) and the registry
// validates it. The core-only binary rejects this file with
// "unknown kind "Catalog" (registered kinds: API, DB, General)".
//
// The outputs type knows only the core keys, so the extension's output key
// carries an expect-error; the registry validates it against the catalog
// generator's OutputSchema.
export default defineConfig({
  name: "shop-catalog",
  kind: "Catalog",
  outputs: {
    // @ts-expect-error catalog is the acme generator's output key
    catalog: { enabled: true }
  }
});
