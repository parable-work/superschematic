import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureTrackerDb } from "@schemas/fixture-tracker-db";

// An issue tracker's API whose operations the build serves from the
// tables of fixture-tracker-db, its one DB dependency (D106): each is a
// declaration marked @tableOperation, and the implementation writes only
// the one operation that is not.
export default defineConfig({
  name: "fixture-tracker-api",
  kind: SchemaKind.API,
  dependencies: [FixtureTrackerDb],
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
