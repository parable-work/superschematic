import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureBlueprintDb } from "@schemas/fixture-blueprint-db";

export default defineConfig({
  name: "fixture-blueprint-api",
  kind: SchemaKind.API,
  // The tables whose behaviors' operations it serves are fixture-blueprint-db's,
  // its one DB dependency (D149). It names no authDb: the database
  // has no User table, so each server takes its caller from the project.
  dependencies: [FixtureBlueprintDb],
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
