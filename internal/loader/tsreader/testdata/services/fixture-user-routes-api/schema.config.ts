import { defineConfig, SchemaKind, service, TargetLanguage } from "@superschematic/schema-config";

export default defineConfig({
  name: "fixture-user-routes-api",
  kind: SchemaKind.API,
  // The user model's routes read their users from the authDb's User and
  // UserRole tables (D50).
  authDb: service({ name: "fixture-user-model-db", kind: SchemaKind.DB }),
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
