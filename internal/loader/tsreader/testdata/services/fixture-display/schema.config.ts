import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The TypeScript twin of fixture-display-json: a General schema whose type
// declares @display over its fields and its Workflow, and its fields'
// titles and icons.
export default defineConfig({
  name: "tickets",
  kind: SchemaKind.General,
  outputs: {}
});
