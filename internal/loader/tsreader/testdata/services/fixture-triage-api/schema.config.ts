import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixturePartiesDb } from "@schemas/fixture-parties-db";

export default defineConfig({
  name: "fixture-triage-api",
  kind: SchemaKind.API,
  // The messages the inbound path holds in triage are fixture-parties-db's,
  // its one DB dependency, whose @conversations tables hold them and whose
  // Contact is the party a held message is settled onto (#746).
  dependencies: [FixturePartiesDb],
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    api: { enabled: true },
    sdk: {
      [TargetLanguage.TypeScript]: { enabled: true }
    }
  }
});
