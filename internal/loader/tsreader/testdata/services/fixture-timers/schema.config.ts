import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The TypeScript twin of fixture-timers-json: a General schema whose
// support requests keep deadlines, escalated or resolved when nobody
// moves them in time, with the core's behaviors, which a binary with no
// extension linked declares.
export default defineConfig({
  name: "requests",
  kind: SchemaKind.General,
  outputs: {}
});
