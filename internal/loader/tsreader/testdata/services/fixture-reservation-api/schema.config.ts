import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureReservationDb } from "@schemas/fixture-reservation-db";

export default defineConfig({
  name: "fixture-reservation-api",
  kind: SchemaKind.API,
  // The tables whose behaviors' operations it serves are
  // fixture-reservation-db's, its one DB dependency (D103, step 5). It
  // names no authDb: the database has no User table, so each server takes
  // its caller from the project.
  dependencies: [FixtureReservationDb],
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
