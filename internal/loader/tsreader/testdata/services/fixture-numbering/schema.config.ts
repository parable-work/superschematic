import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The TypeScript twin of fixture-numbering-json: a General schema whose
// invoices link to the projects of fixture-rollups and are numbered per
// project and per year, with the core's behaviors, which a binary with no
// extension linked declares.
export default defineConfig({
  name: "invoices",
  kind: SchemaKind.General,
  outputs: {}
});
