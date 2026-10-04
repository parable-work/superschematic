import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The TypeScript twin of fixture-variants-json: a General schema whose
// type composes the core's Constants and Variants, which a binary with no
// extension linked declares.
export default defineConfig({
  name: "Step",
  kind: SchemaKind.General,
  outputs: {}
});
