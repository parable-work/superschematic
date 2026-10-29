import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "finances-db",
  kind: SchemaKind.DB,
  outputs: {
    types: { [TargetLanguage.TypeScript]: { enabled: true } },
    // The keys of PlaidItem's Encrypted<T> token, which finances-api's
    // FieldKeys declares (D85).
    sql: { encryption: { key: "FieldKeys.FINANCES_DB_FIELD_KEYS" } }
  }
});
