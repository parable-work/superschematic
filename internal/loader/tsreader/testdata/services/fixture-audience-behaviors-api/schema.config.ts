import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureAudienceBehaviorsDb } from "@schemas/fixture-audience-behaviors-db";

// The behavior operations of a table an audience keeps (D156, D103), served
// in Rust (D181): the tables are fixture-audience-behaviors-db's, its one DB
// dependency. It names no authDb: the database has no User table, so the
// server takes its caller from the project.
export default defineConfig({
  name: "fixture-audience-behaviors-api",
  kind: SchemaKind.API,
  dependencies: [FixtureAudienceBehaviorsDb],
  outputs: {
    types: {
      [TargetLanguage.Rust]: { enabled: true }
    },
    api: { enabled: true, language: "RUST" },
    sdk: {
      [TargetLanguage.Rust]: { enabled: true }
    }
  }
});
