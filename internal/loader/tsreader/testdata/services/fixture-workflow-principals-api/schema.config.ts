import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureWorkflowPrincipalsDb } from "@schemas/fixture-workflow-principals-db";

export default defineConfig({
  name: "fixture-workflow-principals-api",
  kind: SchemaKind.API,
  // The table whose behaviors' operations it serves is
  // fixture-workflow-principals-db's, its one DB dependency (D104). It names
  // no authDb: the database has no User table, so each server takes its
  // caller from the project.
  dependencies: [FixtureWorkflowPrincipalsDb],
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
