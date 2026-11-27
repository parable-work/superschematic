import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// Behaviors on rows an audience keeps (D156, D103): a task its owner may
// keep to themselves or show to the principals it names, whose status is a
// Workflow and which holds a conversation, and a note its author keeps
// private unless it says otherwise, whose summary a deriver writes
// (D146). Their audience ids are a uuid[] the behaviors' core reads with
// the row.
export default defineConfig({
  name: "fixture-audience-behaviors-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { dialects: ["postgres", "sqlite"] }
  }
});
