import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureWorkflowDb } from "@schemas/fixture-workflow-db";

export default defineConfig({
  name: "fixture-workflow-api",
  kind: SchemaKind.API,
  // The tables whose behaviors' operations it serves are fixture-workflow-db's,
  // its one DB dependency (D103). It names no authDb: the database has no
  // User table, so each server takes its caller from the project.
  dependencies: [FixtureWorkflowDb],
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
