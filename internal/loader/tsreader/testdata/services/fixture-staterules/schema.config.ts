import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The TypeScript twin of fixture-staterules-json: a General schema whose
// type composes the core's Workflow and StateRules, which a binary with
// no extension linked declares.
export default defineConfig({
  name: "invoices",
  kind: SchemaKind.General,
  outputs: {}
});
