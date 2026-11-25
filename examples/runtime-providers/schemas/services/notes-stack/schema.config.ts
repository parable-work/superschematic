import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The notes app's stack: each environment names the runtime providers it
// uses (D159).
export default defineConfig({
  name: "notes-stack",
  kind: SchemaKind.Stack,
  outputs: {}
});
