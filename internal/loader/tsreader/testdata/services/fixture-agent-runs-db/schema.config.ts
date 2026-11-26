import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";

// Agent orchestration (D158): tasks an agent works on, with their notes and
// invoices, which a schedule wakes and a principal discusses in a thread;
// customers outside the user table who reach a task through a grant; the
// grants an agent run's token is; the notifier that routes a schedule's
// run to an agent and an approval to the people who may give it; and the
// orchestration's own tables.
export default defineConfig({
  name: "fixture-agent-runs-db",
  kind: SchemaKind.DB,
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    sql: { dialects: ["postgres"] }
  }
});
