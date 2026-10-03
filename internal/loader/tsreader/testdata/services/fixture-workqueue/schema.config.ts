import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The TypeScript twin of fixture-workqueue-json: a General schema whose
// type composes the core's Lease, Assignment, Queue, Budget and Retries,
// which a binary with no extension linked declares.
export default defineConfig({
  name: "jobs",
  kind: SchemaKind.General,
  outputs: {}
});
