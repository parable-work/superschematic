import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// An engine database whose Ticket composes Derived, a model filling its
// category from its subject and body (D73), and whose Note composes
// Comments alone. A deriver of fixture-derived-api fills it (D74).
export default defineConfig({
  name: "fixture-derived-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
