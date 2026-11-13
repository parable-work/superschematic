import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The notes app as one binary (D116): notes-api and its page in one
// process, notes-db a SQLite file under the binary's data directory.
export default defineConfig({
  name: "notes-stack",
  kind: SchemaKind.Stack,
  outputs: {}
});
