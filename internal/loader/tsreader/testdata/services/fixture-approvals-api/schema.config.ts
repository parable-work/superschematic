import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureApprovalsDb } from "@schemas/fixture-approvals-db";

export default defineConfig({
  name: "fixture-approvals-api",
  kind: SchemaKind.API,
  // The table whose behaviors' operations it serves is fixture-approvals-db's,
  // its one DB dependency (D104). It names no authDb: the database has no
  // User table, so each server takes its caller from the project.
  dependencies: [FixtureApprovalsDb],
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
