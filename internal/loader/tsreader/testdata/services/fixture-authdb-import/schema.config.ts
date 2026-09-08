import { defineConfig, SchemaKind } from "@superschematic/schema-config";
import { FixtureDb } from "@schemas/fixture-db";

export default defineConfig({
  name: "fixture-authdb-import",
  kind: SchemaKind.API,
  authDb: FixtureDb,
  dependencies: [FixtureDb],
  outputs: {}
});
