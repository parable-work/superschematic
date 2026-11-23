import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureCalendarDb } from "@schemas/fixture-calendar-db";

export default defineConfig({
  name: "fixture-calendar-api",
  kind: SchemaKind.API,
  // The tables whose behaviors' operations it serves are
  // fixture-calendar-db's, its one DB dependency (D70, D103's step 4). It
  // names no authDb: the database has no User table, so each server takes
  // its caller from the project.
  dependencies: [FixtureCalendarDb],
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
