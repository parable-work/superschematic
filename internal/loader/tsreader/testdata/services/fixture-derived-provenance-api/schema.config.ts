import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureDerivedProvenanceDb } from "@schemas/fixture-derived-provenance-db";

export default defineConfig({
  name: "fixture-derived-provenance-api",
  kind: SchemaKind.API,
  // The tables whose behaviors' operations it serves are
  // fixture-derived-provenance-db's, its one DB dependency (D73, D103's
  // step 4). It names no authDb: the database has no User table, so each
  // server takes its caller from the project.
  dependencies: [FixtureDerivedProvenanceDb],
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
