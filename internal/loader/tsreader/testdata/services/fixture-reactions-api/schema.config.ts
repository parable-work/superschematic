import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureReactionsDb } from "@schemas/fixture-reactions-db";

export default defineConfig({
  name: "fixture-reactions-api",
  kind: SchemaKind.API,
  // The tables whose behaviors' operations it serves, and whose change
  // queues its workers run, are fixture-reactions-db's, its one DB
  // dependency (D103).
  dependencies: [FixtureReactionsDb],
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    },
    api: { enabled: true },
    sdk: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    }
  }
});
