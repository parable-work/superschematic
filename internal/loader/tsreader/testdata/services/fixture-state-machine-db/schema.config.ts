import { defineConfig, SchemaKind, service, TargetLanguage } from "@superschematic/schema-config";

// A support desk's tickets, whose fields move as their state machines say
// (D117): the database refuses any other move, on Postgres and on SQLite,
// and the types of each language carry the same moves.
export default defineConfig({
  name: "fixture-state-machine-db",
  kind: SchemaKind.DB,
  dependencies: [service({ name: "fixture-state-machine-enums", kind: SchemaKind.General })],
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    },
    sql: { dialects: ["postgres", "sqlite"] }
  }
});
