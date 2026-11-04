import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureAgentsDb } from "@schemas/fixture-agents-db";

export default defineConfig({
  name: "fixture-agents-api",
  kind: SchemaKind.API,
  // The tables whose behaviors' operations it serves, and whose deadlines
  // its workers run, are fixture-agents-db's, its one DB dependency (D104).
  // It names no authDb: the database has no User table, so each server takes
  // its caller from the project.
  dependencies: [FixtureAgentsDb],
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    },
    api: { enabled: true },
    sdk: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    }
  }
});
