import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The inbound path over messages that hold no files: the conversations'
// messages keep references and no @attachments, while a report on a job
// holds files, so filing a message onto a row must ask no table for files
// it does not hold.
export default defineConfig({
  name: "fixture-parties-plain-messages-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { dialects: ["postgres", "sqlite"] }
  }
});
