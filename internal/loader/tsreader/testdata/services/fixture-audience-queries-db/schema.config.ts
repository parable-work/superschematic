import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// Declared queries over @audience tables (#735, D86, D156): each query
// marks where a table's audience goes, :audience(d) for the alias d it
// reads the table through, and the ORM replaces the mark with the
// caller's audience, so two callers get each their own rows of one
// query. A query over a join marks both tables, a paged query numbers
// the audience's parameters after its page key's, and a query over a
// projection's view marks the view. The Go and TypeScript ORMs, on
// Postgres.
export default defineConfig({
  name: "fixture-audience-queries-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Go]: { enabled: true }
    },
    sql: { dialects: ["postgres"] }
  }
});
