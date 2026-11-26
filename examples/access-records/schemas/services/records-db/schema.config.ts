import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// The tables of a records app with per-record access: workspaces and pages
// with an audience, agreements whose tickets a row rule reaches through
// relations, the grants that let a party reach rows, and the access log.
export default defineConfig({
  name: "records-db",
  kind: SchemaKind.DB,
  outputs: {
    // Go types too, as every DB service has: the DB kind always generates
    // the Go ORM, which imports them.
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    // The keys of Agreement's Encrypted<T> terms (D85), which records-api's
    // FieldKeys declares; the server hands them to createOrm.
    sql: { dialects: ["postgres"], encryption: { key: "FieldKeys.RECORDS_DB_FIELD_KEYS" } }
  }
});
