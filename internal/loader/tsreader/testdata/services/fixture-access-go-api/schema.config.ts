import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureAccessGoDb } from "@schemas/fixture-access-go-db";

// fixture-access-api as a Go server (D180): every read keeps to its
// caller's audience and grants, a row rule follows a path through
// relations, and a contract's terms open only for a holder of the reveal
// permission.
export default defineConfig({
  name: "fixture-access-go-api",
  kind: SchemaKind.API,
  dependencies: [FixtureAccessGoDb],
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true }
    },
    api: { enabled: true }
  }
});
