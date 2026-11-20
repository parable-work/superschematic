import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureNumberingDb } from "@schemas/fixture-numbering-db";

export default defineConfig({
  name: "fixture-numbering-api",
  kind: SchemaKind.API,
  // The tables whose behaviors' operations it serves are
  // fixture-numbering-db's, its one DB dependency (D103's step 4). It
  // names no authDb: the database has no User table, so each server takes
  // its caller from the project.
  dependencies: [FixtureNumberingDb],
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
