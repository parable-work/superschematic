import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// Exact decimals, signed money and vectors in a database (D84): NUMERIC(p,s)
// and BIGINT columns, vector(n) columns with the vector extension, and an
// HNSW and an IVFFlat index the nearest-neighbour queries use; and a table
// @table and @column name, with an Encrypted<T> field beside its vector
// (D85).
export default defineConfig({
  name: "fixture-vectors-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    },
    // The keys of Chunk's Encrypted<T> note (D85).
    sql: { encryption: { key: "VectorKeys.VECTOR_DB_FIELD_KEYS" } }
  }
});
