import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The TypeScript twin of fixture-branches-json: a General schema whose
// type composes the core's Branches, which a binary with no extension
// linked declares.
export default defineConfig({
  name: "Recipe",
  kind: SchemaKind.General,
  outputs: {}
});
