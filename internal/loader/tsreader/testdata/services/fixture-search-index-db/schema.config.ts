import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// One search index per environment (#740): the search database a stack
// names with @stack({ search }), which holds the environment's search
// index, the copy of its source databases' index rows. Its words fold
// diacritics, as its sources' do. Postgres alone.
export default defineConfig({
  name: "fixture-search-index-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { search: { environmentIndex: true, foldDiacritics: true } }
  }
});
