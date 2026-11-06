import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureSchedulesDb } from "@schemas/fixture-schedules-db";

export default defineConfig({
  name: "fixture-schedules-api",
  kind: SchemaKind.API,
  // The table whose behaviors' operations it serves, and whose deadlines,
  // heard writes and events its worker runs, is fixture-schedules-db's, its
  // one DB dependency (D104). It names no authDb: the database has no User
  // table, so each server takes its caller from the project.
  dependencies: [FixtureSchedulesDb],
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
