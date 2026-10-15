import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// A catalog whose lists page by a cursor (D60): a page of an object type
// with query arguments beside it, a page of a primitive with none, and the
// operations around them that do not page.
export default defineConfig({
  name: "fixture-paged-api",
  kind: SchemaKind.API,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    },
    api: { enabled: true },
    sdk: {
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    }
  }
});
