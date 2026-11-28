import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// A database whose Ticket table implements Derived, a model filling its
// category from its subject and body (D73, D146), and whose Note
// implements Comments alone. A deriver of fixture-derived-api fills it.
export default defineConfig({
  name: "fixture-derived-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
