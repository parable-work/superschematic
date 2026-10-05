import { defineConfig, SchemaKind, service } from "@superschematic/schema-config";

// An API whose implementation calls another API: calls reaches the IR
// beside authDb and dependencies.
export default defineConfig({
  name: "fixture-calls",
  kind: SchemaKind.API,
  calls: [service({ name: "fixture-api", kind: SchemaKind.API })],
  outputs: {}
});
