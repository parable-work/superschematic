import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureRecurrenceDb } from "@schemas/fixture-recurrence-db";

export default defineConfig({
  name: "fixture-recurrence-api",
  kind: SchemaKind.API,
  // The table whose Recurrence operations it serves, and whose deadlines
  // and heard writes its worker runs, is fixture-recurrence-db's, its one
  // DB dependency (D152). It names no authDb: the database has no User
  // table, so each server takes its caller from the project.
  dependencies: [FixtureRecurrenceDb],
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    },
    // A TypeScript server: the Go server serves neither Recurrence nor a
    // calendar source yet (D180).
    api: { enabled: true, language: "TYPESCRIPT" },
    sdk: {
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    }
  }
});
