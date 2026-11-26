import { defineConfig, SchemaKind } from "@superschematic/schema-config";

// The work app's stack: work-api, whose generated server builds the agent
// dispatcher, mounts its routes and runs its queues (D183), on this
// machine.
export default defineConfig({
  name: "work-stack",
  kind: SchemaKind.Stack,
  outputs: {}
});
