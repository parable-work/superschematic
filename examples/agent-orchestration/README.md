# agent-orchestration

A work app whose agents are rows: each run gets a fresh sandbox instance
and a token held to its task, asks before it moves money, spends within a
budget, and stops when someone pulls the switch (D158). The
[Agent runs](https://parable-work.github.io/superschematic/guides/agent-runs/)
guide covers it in full.

| Path | What it is |
|---|---|
| `schemas/services/work-db` | DB, Postgres: `Task` (a schedule wakes an agent on it, a person and an agent talk in its thread), `TaskNote` and `Invoice`, which a run on a task reaches through its role, `Task` and `TaskNote` `@searchable` on the queue `SearchIndexing`; `Customer`, a party outside the user table (D157), and `Grant`, the grants a customer's and a run's token are; the notifier's tables; and the orchestration's: `Agent` (`@versioned` definitions), `AgentRun` (runs, holding a Lease while their instance runs and started again by Retries), `AgentRunRequest` (the dispatcher's queue), `AutonomyPolicy`, `AgentApproval`, `AgentControl` (each agent's stop switch and budget) and `AgentBudget` (every agent's); and D157's `Conversation`, `Message` and `RunRequest`, the queue a customer's inbound messages put run requests on |
| `schemas/services/work-api` | API over `work-db`, served in TypeScript: `WorkApiConfig` names a sandbox, a model, a credential broker and an SMS channel slot, and where an instance reaches the agent routes; `getTask` and `payInvoice` say what they are to an agent's autonomy, `payInvoice` moving money; `Dispatch` runs the dispatcher on its queue, `InboundRuns` hands it each run request a customer's message made, `Notify` the notifier, `TaskDeadlines` each task's schedules and `RunDeadlines` each run's lease and backoff |
| `schemas/services/work-stack` | Stack: `work-api` on this machine, its provider slots bound in `Dev`'s env |
| `typescript/work-api` | `work-api`'s implementation: `createAgentOptions`, what the dispatcher is built with beside its tables (the providers the config names, the notifier, the inbound path, the app's tools); the operations, each an agent's call held to its run's autonomy; and the workers the app runs itself |

The generated server does the rest of the wiring (D183): it builds the
dispatcher into `work-api`'s `Deps` (`deps.agents`), mounts the agent
routes an instance calls, and hands each message of `Dispatch`'s and
`InboundRuns`' queues to the dispatcher, so the implementation has no
method for either.

The providers are `examples/runtime-providers`'s: the local-process
sandbox, the model stub, the vault broker and the first SMS relay.
Swapping the sandbox for a container platform's, or the model for a model
API's, is config alone, though no such provider ships yet.

## What it shows

- **A run's reach is its task's.** A schedule on a task fires; a route
  whose recipients name `{ agent: "reviewer" }` puts a run request on the
  dispatcher's queue. The run's token is a grant per target row, holding the
  agent's role, whose `reaches` (`TaskNote:task`, `Invoice:task`) say what
  else it reads. Its search across tables finds the task and its note, and
  nothing of another task.
- **Money waits for a person.** `payInvoice` is `@autonomy("moneyMovement")`:
  an agent's call writes an approval holding the exact invoice and amount and
  their hash, and the approver's phone gets a text. "approve 1" runs it;
  a call with any other amount asks again. A policy row or an `@autonomy`
  mode that would let money move on its own is refused.
- **Budgets pause.** Each turn's tokens count on the agent's `AgentControl`
  meter, which counts on `AgentBudget` too. A warning goes out at the row's
  `warnAt` percent; at the cap the agent pauses; at every agent's cap
  scheduled runs stop and a person's run waits for an approval.
- **One switch stops everything.** Pausing every agent cancels each run in
  flight through the sandbox provider, revokes its token and puts its
  assigned task back in the queue.
- **Credentials stay in the sandbox.** A definition names references alone.
  The instance resolves them with `useCredential` into the request that
  needs them; the run records the reference, the target and the time, and
  the transcript holds no value.
- **A customer's text starts a run.** It takes D157's one inbound path: a
  message row and a run request on `RunRequest`, which the dispatcher takes
  as it is. The run acts for the customer, narrowed to its grant's role,
  and reaches its task, its row and the message, and nothing through a
  relation path its grant's role does not reach (D183). A customer whose number was
  entered by hand, not verified, starts a run that reads, and asks before
  every write it makes, whatever the policy says. Writing back is
  `message_party`, class `messageParty`: D157's send holds the message
  until the approval its `approve` hook asked for (`agents.sendApproval`)
  is given.
- **Outside content is data.** A web page an instance fetched and a
  customer's inbound message reach the model marked untrusted; what they
  say to do is not done.

## Where it runs

`TestAgentOrchestrationExampleLoads` and `TestAutonomyRefusesMoneyWithoutAsking`
(`internal/loader/agents`) load these schemas.
`TestAgentOrchestrationExampleUsesTheGeneratedWiring` (`cmd/superschematic`)
builds the stack and type-checks the generated server and workers against
the implementation. The same scenario, under the
fixture name `fixture-agent-runs-db`, runs end to end against Postgres in
`TestAgentRunsEndToEnd` (`internal/generator/tsgen`): the dispatcher starts
each instance in the local-process sandbox, which runs the runtime's
`runAgentInstance` under Bun, completing its turns through the model stub.

The dispatcher is the TypeScript runtime's, on Postgres: the Go and Rust
servers have no dispatcher or autonomy yet. The generated TypeScript server
builds it into `work-api`'s `Deps` (D183), and `typescript/work-api` gives
it the options only the app knows.
