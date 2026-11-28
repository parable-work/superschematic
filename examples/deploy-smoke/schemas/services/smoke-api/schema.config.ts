import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { SmokeDb } from "@smoke/smoke-db";
import { SmokeFiles } from "@smoke/smoke-files";

// The smoke's API, shaped as a finances API is: served in TypeScript over
// smoke-db, which holds its users, with cookie sessions, a worker that
// drains smoke-db's queue, a job named as a finances API names its own,
// and the bucket a line item's files are uploaded to. A TypeScript server runs on Cloud Run and on Workers alike.
// Its one provider package is the stub relay, the channel adapter of its
// SMS slot (D159).
export default defineConfig({
  name: "smoke-api",
  kind: SchemaKind.API,
  authDb: SmokeDb,
  dependencies: [SmokeDb],
  buckets: [SmokeFiles],
  providers: ["../../../providers/stub-relay"],
  outputs: {
    types: { [TargetLanguage.TypeScript]: { enabled: true } },
    api: { enabled: true, language: "TYPESCRIPT" },
    sdk: { [TargetLanguage.TypeScript]: { enabled: true } }
  }
});
