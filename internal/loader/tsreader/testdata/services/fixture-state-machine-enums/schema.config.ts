import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// Owns an enum a database's state machine governs a field of (D118).
export default defineConfig({
  name: "fixture-state-machine-enums",
  kind: SchemaKind.General,
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    }
  }
});
