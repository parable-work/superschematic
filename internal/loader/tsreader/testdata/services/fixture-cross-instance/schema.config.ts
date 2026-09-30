import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The TypeScript twin of fixture-cross-instance-json: a General schema
// whose type composes the core's cross-instance behaviors, which a binary
// with no extension linked declares.
export default defineConfig({
  name: "tasks",
  kind: SchemaKind.General,
  outputs: {}
});
