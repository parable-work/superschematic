import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The TypeScript twin of fixture-commerce-json: a General schema whose
// inventory, collection points, checkouts and refunds compose the core's
// commerce behaviors (D70), which a binary with no extension linked
// declares.
export default defineConfig({
  name: "shop",
  kind: SchemaKind.General,
  outputs: {}
});
