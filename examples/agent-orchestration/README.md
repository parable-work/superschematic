# agent-orchestration

A work app whose agents are rows: each run gets a fresh sandbox instance
and a token held to its task, asks before it moves money, spends within a
budget, and stops when someone pulls the switch (D158).

| Path | What it is |
|---|---|
| `schemas/services/work-db` | DB, Postgres: `Task` (a schedule wakes an agent on it, a person and an agent talk in its thread), `TaskNote` and `Invoice`, which a run on a task reaches through its role; `Customer`, a party outside the user table, and `Grant`, the grants a customer's and a run's token are; the notifier's tables; and the orchestration's: `Agent` (`@versioned` definitions), `AgentRun` (runs, holding a Lease while their instance runs and started again by Retries), `AgentRunRequest` (the dispatcher's queue), `AutonomyPolicy`, `AgentApproval`, `AgentControl` (each agent's stop switch and budget) and `AgentBudget` (every agent's); `Customer` is a party (D157), with `Conversation`, `Message` and `RunRequest`, the queue its inbound messages put run requests on |
| `schemas/services/work-api` | API over `work-db`: `WorkApiConfig` names a sandbox, a model, a credential broker and an SMS channel slot; `getTask` and `payInvoice` say what they are to an agent's autonomy, `payInvoice` moving money; `Dispatch` runs the dispatcher on its queue, `InboundRuns` hands it each run request a customer's message made, `Notify` the notifier, `TaskDeadlines` each task's schedules |
| `server/server.ts` | The wiring: the dispatcher and the notifier over the providers the config names, the agent routes an instance calls, and the workers' handlers |

The providers are `examples/runtime-providers`'s: the local-process
sandbox, the model stub, the vault broker and the first SMS relay. A real
deployment swaps the sandbox for a container platform's and the model for a
model API's, by config alone.

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
  and reaches its task, its row and the message. Writing back is
  `message_party`, class `messageParty`: D157's send holds the message
  until the approval its `approve` hook asked for (`agents.sendApproval`)
  is given.
- **Outside content is data.** A web page an instance fetched and a
  customer's inbound message reach the model marked untrusted; what they
  say to do is not done.

## Where it runs

`TestAgentOrchestrationExampleLoads` and `TestAutonomyRefusesMoneyWithoutAsking`
(`internal/loader/agents`) load these schemas. The same scenario, under the
fixture name `fixture-agent-runs-db`, runs end to end against Postgres in
`TestAgentRunsEndToEnd` (`internal/generator/tsgen`): the dispatcher starts
each instance in the local-process sandbox, which runs the runtime's
`runAgentInstance` under Bun, completing its turns through the model stub.
