import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The jobs app's tables (D157): contacts outside the organisation, who are
// parties and reach the jobs granted them; the conversations their texts
// and emails are rows of; the run requests a granted party's message
// makes; the notifier's tables a user's reply runs through; and the
// channel identities every sender resolves through. Postgres alone.
export default defineConfig({
  name: "jobs-db",
  kind: SchemaKind.DB,
  outputs: {
    // Go types too, as every DB service has: the DB kind always generates
    // the Go ORM, which imports them.
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { dialects: ["postgres"] }
  }
});
