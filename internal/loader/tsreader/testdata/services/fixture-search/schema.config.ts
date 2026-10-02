import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The TypeScript twin of fixture-search-json: a General schema whose type
// composes the core's Search, which a binary with no extension linked
// declares.
export default defineConfig({
  name: "notes",
  kind: SchemaKind.General,
  outputs: {}
});
