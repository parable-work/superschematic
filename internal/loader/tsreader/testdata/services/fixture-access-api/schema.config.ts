import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureAccessDb } from "@schemas/fixture-access-db";

// An API whose operations the build serves from the tables of
// fixture-access-db (D156): every read keeps to its caller's audience and
// grants, a row rule follows a path through relations, and a contract's
// terms open only for a holder of the reveal permission.
export default defineConfig({
  name: "fixture-access-api",
  kind: SchemaKind.API,
  dependencies: [FixtureAccessDb],
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
