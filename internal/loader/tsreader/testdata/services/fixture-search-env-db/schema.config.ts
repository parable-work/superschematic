import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// One search index per environment (#740): a source database whose
// @searchable tables' index rows its worker copies into the environment's
// index, which fixture-search-index-db holds. Five @searchable tables, two
// with an audience, one soft-deleting, one scoped; the grants a party
// reaches rows through; and the queue every write of a row or a grant
// enqueues on. Postgres alone, its words folding diacritics as the index's.
export default defineConfig({
  name: "fixture-search-env-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { search: { environment: true, foldDiacritics: true } }
  }
});
