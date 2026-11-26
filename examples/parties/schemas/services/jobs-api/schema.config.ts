import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { JobsDb } from "@schemas/jobs-db";

// The jobs app's API, served from jobs-db's tables in TypeScript. Its
// channel adapters are the runtime-providers example's SMS and mail
// relays, which also read what contacts send back.
export default defineConfig({
  name: "jobs-api",
  kind: SchemaKind.API,
  dependencies: [JobsDb],
  providers: ["../../../../runtime-providers/providers/sms-relay-a", "../../../../runtime-providers/providers/mail-relay"],
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    api: { enabled: true }
  }
});
