import { defineConfig, SchemaKind, service } from "@superschematic/schema-config";

// A relation's key (D61, amended): a field <r>Id of an @source type reads
// the key of the source's Relation r without @virtual, and refers to the
// API's one type that is @source of the related table. Nothing writes
// the reference, so format writes none.
export default defineConfig({
  name: "fixture-refs-keys-api",
  kind: SchemaKind.API,
  dependencies: [service({ name: "fixture-db", kind: SchemaKind.DB })],
  outputs: {}
});
