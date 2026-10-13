import { defineConfig, SchemaKind } from "@superschematic/schema-config";

export default defineConfig({
  name: "notes-stack",
  kind: SchemaKind.Stack,
  outputs: { ci: { github: {} } }
});
