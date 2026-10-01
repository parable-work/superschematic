import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The TypeScript twin of fixture-behaviors-json: a General schema whose
// type composes the core's behaviors, which a binary with no extension
// linked declares.
export default defineConfig({
  name: "documents",
  kind: SchemaKind.General,
  outputs: {}
});
