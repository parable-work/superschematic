import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The notes a person keeps, in a SQLite file: the DB service lists sqlite
// first, so notes-api's Deps.db is the file the binary opens under its
// data directory (D116). It lists postgres and generates Go types too, as
// every DB service does: the DB kind always generates the Go ORM, which
// runs on Postgres and imports them.
export default defineConfig({
  name: "notes-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    },
    sql: { dialects: ["sqlite", "postgres"] }
  }
});
