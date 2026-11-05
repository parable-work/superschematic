import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureSeatsDb } from "@schemas/fixture-seats-db";

export default defineConfig({
  name: "fixture-seats-api",
  kind: SchemaKind.API,
  // The tables whose behaviors' operations it serves, and whose deadlines
  // its worker runs, are fixture-seats-db's, its one DB dependency (D104).
  // It names no authDb: the database has no User table, so each server takes
  // its caller from the project.
  dependencies: [FixtureSeatsDb],
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
