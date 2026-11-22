import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// An @envVars class that composes two plain env classes: one as it is and
// one through Partial.
export default defineConfig({
  name: "fixture-env-composed",
  kind: SchemaKind.General,
  outputs: {}
});
