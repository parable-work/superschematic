import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureReferencesDb } from "@schemas/fixture-references-db";
import { FixtureReferencesFiles } from "@schemas/fixture-references-files";

export default defineConfig({
  name: "fixture-references-api",
  kind: SchemaKind.API,
  // The tables whose references and files it serves are
  // fixture-references-db's, its one DB dependency (#723). It names no
  // authDb: the database has no User table, so each server takes its
  // caller from the project. The files are in its one bucket.
  dependencies: [FixtureReferencesDb],
  buckets: [FixtureReferencesFiles],
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
