import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { SmokeDb } from "@smoke/smoke-db";

// The smoke's API, shaped as a finances API is: served in TypeScript over
// smoke-db, which holds its users, with cookie sessions, a worker that
// drains smoke-db's queue and a job named as a finances API names its
// own. A TypeScript server runs on Cloud Run and on Workers alike.
export default defineConfig({
  name: "smoke-api",
  kind: SchemaKind.API,
  authDb: SmokeDb,
  dependencies: [SmokeDb],
  outputs: {
    types: { [TargetLanguage.TypeScript]: { enabled: true } },
    api: { enabled: true, language: "TYPESCRIPT" },
    sdk: { [TargetLanguage.TypeScript]: { enabled: true } }
  }
});
