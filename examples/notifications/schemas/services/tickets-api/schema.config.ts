import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { TicketsDb } from "@schemas/tickets-db";

// The tickets app's API, served from tickets-db's tables in TypeScript.
// Its channel adapters are the runtime-providers example's SMS and mail
// relays.
export default defineConfig({
  name: "tickets-api",
  kind: SchemaKind.API,
  dependencies: [TicketsDb],
  providers: ["../../../../runtime-providers/providers/sms-relay-a", "../../../../runtime-providers/providers/mail-relay"],
  outputs: {
    types: {
      [TargetLanguage.TypeScript]: { enabled: true }
    },
    api: { enabled: true }
  }
});
