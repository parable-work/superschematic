import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// Declared, type-checked queries (D86): a household ledger's dashboard
// reports as @query declarations, its category totals rolled up to the
// parent, its spending in generate_series time buckets, its transaction
// list paged by a keyset, a list in a sort an enum picks, its budgets with
// exact decimals and dates, and the transfer matcher's candidates.
export default defineConfig({
  name: "fixture-queries-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Go]: { enabled: true }
    }
  }
});
