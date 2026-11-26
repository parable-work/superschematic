import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureVersionGraphDb } from "@schemas/fixture-version-graph-db";

export default defineConfig({
  name: "fixture-graph-api",
  kind: SchemaKind.API,
  // The version graph whose operations it serves is fixture-version-graph-db's,
  // its one DB dependency (D138). It names no authDb: the database has no
  // User table, so each server takes its caller from the project. Its types
  // are generated in the languages the database's are.
  dependencies: [FixtureVersionGraphDb],
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    api: { enabled: true },
    sdk: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
