import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The TypeScript twin of fixture-display-json: a General schema whose type
// declares @display over its fields and its Workflow, with an icon, its
// fields' titles, icons and multiline hint, and an enum whose members take
// titles.
export default defineConfig({
  name: "tickets",
  kind: SchemaKind.General,
  outputs: {}
});
