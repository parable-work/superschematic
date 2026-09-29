import { defineConfig, SchemaKind, service, TargetLanguage } from "@superschematic/schema-config";

// A public API whose authDb, fixture-chain-db, is two hops from
// fixture-chain-base: the API module reaches base through the db ORM and
// through its own types.
export default defineConfig({
  name: "fixture-chain-api",
  kind: SchemaKind.API,
  public: true,
  authDb: service({ name: "fixture-chain-db", kind: SchemaKind.DB }),
  dependencies: [service({ name: "fixture-chain-common", kind: SchemaKind.General })],
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true }
    },
    api: { enabled: true },
    sdk: {
      [TargetLanguage.Go]: { enabled: true }
    }
  }
});
