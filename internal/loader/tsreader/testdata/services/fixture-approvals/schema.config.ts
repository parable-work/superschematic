import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The TypeScript twin of fixture-approvals-json: a General schema whose
// expense claims wait for sign-offs before they are approved and paid,
// with the core's behaviors, which a binary with no extension linked
// declares.
export default defineConfig({
  name: "expenses",
  kind: SchemaKind.General,
  outputs: {}
});
