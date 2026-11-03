import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The smoke's stack (D97): smoke-api and its site on one origin (D91), on
// this machine, on Google Cloud and on Cloudflare.
export default defineConfig({
  name: "smoke-stack",
  kind: SchemaKind.Stack,
  outputs: {}
});
