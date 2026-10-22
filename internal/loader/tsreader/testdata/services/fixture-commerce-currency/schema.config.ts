import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The TypeScript twin of fixture-commerce-currency-json: Stock and
// Payments read the currency from a Finance.Currency own field (D70).
export default defineConfig({
  name: "billing",
  kind: SchemaKind.General,
  outputs: {}
});
