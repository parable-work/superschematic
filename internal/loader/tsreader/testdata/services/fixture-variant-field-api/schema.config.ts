import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureVariantFieldDb } from "@schemas/fixture-variant-field-db";

// The API of fixture-variant-field-db's steps, whose result is a
// discriminated-union field (D149): every server checks a body's result
// by its kind, and every SDK types it.
export default defineConfig({
  name: "fixture-variant-field-api",
  kind: SchemaKind.API,
  dependencies: [FixtureVariantFieldDb],
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    },
    api: { enabled: true },
    sdk: {
      [TargetLanguage.Go]: { enabled: true },
      [TargetLanguage.TypeScript]: { enabled: true },
      [TargetLanguage.Python]: { enabled: true },
      [TargetLanguage.Rust]: { enabled: true }
    }
  }
});
