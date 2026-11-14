import { defineConfig, SchemaKind } from "@superschematic/schema-config";

export default defineConfig({
  name: "broken-fenced-optimistic-sqlite",
  kind: SchemaKind.DB,
  outputs: { sql: { dialects: ["postgres", "sqlite"] } }
});
