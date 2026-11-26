import { WorkApi } from "@schemas/work-api";
import { environment, stack } from "@superschematic/stack";

// work-api over work-db. Its server, its dispatcher's workers (Dispatch
// and InboundRuns), its notifier's (Notify) and its tasks' schedules'
// (TaskDeadlines) are each a deployable of the stack (D158, D183).
@stack({ deploy: [WorkApi] })
export abstract class Work {}

// This machine: the local-process sandbox and the model stub run each
// agent's turns, the vault broker resolves its credentials and the first
// SMS relay asks for its approvals, each in front of a stub on loopback.
// The secret settings, CREDENTIALS_TOKEN and SMS_TOKEN, are entered with
// `stack secrets set`.
@environment({
  target: "local",
  settings: [
    {
      of: WorkApi,
      env: {
        SANDBOX: "local-sandbox",
        MODEL: "stub-model",
        CREDENTIALS: "vault-broker",
        CREDENTIALS_URL: "http://127.0.0.1:4320",
        CREDENTIALS_VAULTS: "ops",
        SMS: "sms-relay-a",
        SMS_URL: "http://127.0.0.1:4310",
        AGENT_ENDPOINT: "http://127.0.0.1:8080/api/agents",
      },
    },
  ],
})
export abstract class Dev {}
