import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureWorkerRefsDb } from "@schemas/fixture-worker-refs-db";

export default defineConfig({
  name: "fixture-worker-refs-api",
  kind: SchemaKind.API,
  // The table whose events its worker hands to the project's Workers is
  // fixture-worker-refs-db's, whose ORM holds references and attachments
  // (D154), its one DB dependency.
  dependencies: [FixtureWorkerRefsDb],
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    api: { enabled: true }
  }
});
