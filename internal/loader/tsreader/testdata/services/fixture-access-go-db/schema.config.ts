import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// fixture-access-db for the Go outputs (D180): the tables of per-record
// access (D156) that fixture-access-go-api serves from a generated Go
// server, the calendar and its feed among them (D152, D187).
export default defineConfig({
  name: "fixture-access-go-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true }
    },
    // The keys of Contract's sealed terms, which fixture-access-go-api's
    // @envVars class declares.
    sql: { dialects: ["postgres"], encryption: { key: "AccessKeys.ACCESS_GO_DB_FIELD_KEYS" } }
  }
});
