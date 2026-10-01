import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The TypeScript twin of fixture-rollups-json: a General schema whose type
// rolls up the tasks of fixture-cross-instance that point at it, with the
// core's behaviors, which a binary with no extension linked declares.
export default defineConfig({
  name: "projects",
  kind: SchemaKind.General,
  outputs: {}
});
