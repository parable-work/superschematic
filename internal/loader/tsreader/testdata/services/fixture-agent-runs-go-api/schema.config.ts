import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
import { FixtureAgentRunsDb } from "@schemas/fixture-agent-runs-db";

// A Go server over fixture-agent-runs-db whose generated wiring builds the
// notifier, the inbound path and the agent dispatcher into its Deps, mounts
// the inbox, the relays' callbacks, the agent routes and search, and runs
// the queues of all four (D190, D192, D187). Its channel is the SMS relay
// of examples/runtime-providers, and its runs' sandbox and model that
// example's local-process sandbox and model stub.
export default defineConfig({
  name: "fixture-agent-runs-go-api",
  kind: SchemaKind.API,
  dependencies: [FixtureAgentRunsDb],
  providers: [
    "../../../../../../examples/runtime-providers/providers/sms-relay-a",
    "../../../../../../examples/runtime-providers/providers/local-sandbox",
    "../../../../../../examples/runtime-providers/providers/stub-model"
  ],
  outputs: {
    types: {
      [TargetLanguage.Go]: { enabled: true }
    },
    api: { enabled: true }
  }
});
