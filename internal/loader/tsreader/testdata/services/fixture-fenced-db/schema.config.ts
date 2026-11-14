import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// A dispatch desk's tables, whose counters every covered write advances by
// one (D119): the database refuses a write that does not, on Postgres and
// on SQLite, and the repositories of each language advance them
// themselves.
export default defineConfig({
  name: "fixture-fenced-db",
  kind: SchemaKind.DB,
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
