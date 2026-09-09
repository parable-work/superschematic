import { defineConfig } from "@superschematic/schema-config";

// An extension kind and output key: kind admits any registered kind name
// (SchemaKindName), while the outputs type knows only the core keys, so that
// one is asserted. The registry validates both values.
export default defineConfig({
  name: "shop",
  kind: "Catalog",
  outputs: {
    // @ts-expect-error catalog is the acme generator's output key
    catalog: { enabled: true }
  }
});
