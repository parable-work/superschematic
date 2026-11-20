import { NotesDb } from "@notes/notes-db";
import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The notes' API, served in Rust from the tables of notes-db, its one DB
// dependency (D106): each operation is a declaration marked
// @tableOperation, which the build serves from the database's Rust
// repositories on SQLite (D116), so the implementation writes none.
export default defineConfig({
  name: "notes-api",
  kind: SchemaKind.API,
  dependencies: [NotesDb],
  outputs: {
    types: {
      [TargetLanguage.Rust]: { enabled: true }
    },
    // mcp serves its visible tools, each table operation and behavior
    // operation among them, at POST /api/mcp, an endpoint the binary
    // answers under /api/notes-api (D81, D111, D116).
    api: { enabled: true, language: "RUST", mcp: true },
    // notes-digest calls it through its Rust SDK.
    sdk: {
      [TargetLanguage.Rust]: { enabled: true }
    }
  }
});
