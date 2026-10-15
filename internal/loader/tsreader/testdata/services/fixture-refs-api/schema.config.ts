import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// References and a search (D61, D62): fields and arguments that name an
// item of the catalog, and the catalog's list, which a search narrows and
// whose ids argument is the references' lookup. Its loaders' twins are
// internal/loader/testdata/services/fixture-refs-api-json and -yaml.
export default defineConfig({
  name: "fixture-refs-api",
  kind: SchemaKind.API,
  outputs: {}
});
