import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// Approvals that read each signer's permission again at each guard
// (D200): a change a reviewer signs off, whose gate counts a sign-off only
// while its signer still holds changes.approve through the roles the
// identity tables grant them.
export default defineConfig({
  name: "fixture-approvals-identity-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { dialects: ["postgres", "sqlite"] }
  }
});
