import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The TypeScript twin of fixture-reactions-json: a General schema whose
// type composes the core's Reactions, which a binary with no extension
// linked declares.
export default defineConfig({
  name: "projects",
  kind: SchemaKind.General,
  outputs: {}
});
