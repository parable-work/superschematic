import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureWorkflowDb } from "@schemas/fixture-workflow-db";

export default defineConfig({
  name: "fixture-workflow-view-api",
  kind: SchemaKind.API,
  // A view of a table whose fields are behaviors (D103), which projects
  // the fields its status Workflow owns.
  dependencies: [FixtureWorkflowDb],
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    api: { enabled: true }
  }
});
