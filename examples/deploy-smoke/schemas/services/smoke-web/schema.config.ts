import { defineConfig, SchemaKind } from "@superschematic/schema-config";
import { SmokeApi } from "@smoke/smoke-api";

// The smoke's site: a page at web/smoke-web that says who is signed in.
// The stack serves smoke-api on its origin, under /api/smoke-api (D91).
export default defineConfig({
  name: "smoke-web",
  kind: SchemaKind.Site,
  calls: [SmokeApi],
  site: { build: "build", output: "dist", fallback: "index.html" }
});
