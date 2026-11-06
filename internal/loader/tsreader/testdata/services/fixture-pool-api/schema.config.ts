import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixturePoolDb } from "@schemas/fixture-pool-db";

export default defineConfig({
  name: "fixture-pool-api",
  kind: SchemaKind.API,
  // The table whose behaviors' operations it serves, and whose deadlines
  // and events its worker runs, is fixture-pool-db's, its one DB dependency
  // (D104). It names no authDb: the database has no User table, so each
  // server takes its caller from the project.
  dependencies: [FixturePoolDb],
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
