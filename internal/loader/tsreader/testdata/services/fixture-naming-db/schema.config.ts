import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// A database whose tables and columns an existing application named in
// camel case (D85): @table and @column keep its names, quoted wherever
// they must be, through the DDL and both ORMs.
export default defineConfig({
  name: "fixture-naming-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Go]: { enabled: true }
    }
  }
});
