import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureTimersDb } from "@schemas/fixture-timers-db";

export default defineConfig({
  name: "fixture-timers-api",
  kind: SchemaKind.API,
  // The table whose behaviors' operations it serves, and whose deadlines
  // its worker runs, is fixture-timers-db's, its one DB dependency (D103).
  dependencies: [FixtureTimersDb],
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
