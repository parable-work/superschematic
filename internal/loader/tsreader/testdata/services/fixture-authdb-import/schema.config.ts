import { defineConfig, SchemaKind } from "@psgen/schema-config";
import { FixtureDb } from "@parable-platform/fixture-db";

export default defineConfig({
  name: "fixture-authdb-import",
  kind: SchemaKind.API,
  authDb: FixtureDb,
  dependencies: [FixtureDb],
  outputs: {}
});
