import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// A report a caller exports as an Arrow IPC stream (D167): an operation whose
// success body is a stream, not the JSON envelope, beside one that answers it.
export default defineConfig({
  name: "fixture-stream-api",
  kind: SchemaKind.API,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    },
    api: { enabled: true, language: "GO" },
    sdk: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    }
  }
});
