---
title: Agent runs
description: Keep agents as rows, start each run in a fresh sandbox with a token its task bounds, set what an agent may do on its own per operation, and stop it with budgets and switches.
sidebar:
  order: 5.695
---

An agent is a row. Something starts a run of it: a route, an assignment, a
schedule, a person, or a message from a party. The dispatcher gives the run
a fresh sandbox instance and a token that reaches the run's rows and
nothing else. Each operation the agent calls passes its autonomy policy,
which may say to ask a person first. Budgets meter what runs spend, and a
stop switch ends every run in flight.

| Built | Not built yet |
| --- | --- |
| `@agents("<part>")` on the orchestration's tables, which the build fills | Go and Rust runtimes; SQLite and D1 |
| run requests on a queue from routes, assignments, schedules, principals and inbound messages | a generated server that builds the dispatcher into its `Deps` and mounts its routes |
| the run's token as a grant per target row, acting for its principal | `@autonomy` carried into the generated MCP tool records |
| autonomy per agent and operation or class, approvals bound to their parameters' hash | a real container platform's sandbox provider and a model API's provider |
| budgets per agent and in total, warnings, pauses; stop switches | a live run on Workers (`smoke.sh Cloudflare` is the maintainer's) |
| asking in a row's thread, with a follow-up row at the deadline | an agent that hands work to another agent |
| redacted transcripts in a bucket; credentials that stay in the sandbox | |

D158 in [docs/DECISIONS.md](https://github.com/parable-work/superschematic/blob/main/docs/DECISIONS.md)
records the design and what is left. `examples/agent-orchestration` is
the worked example this page quotes.

## Declare the tables

The orchestration keeps its state in classes the database declares, one
per part. The build adds each part's fields the class does not declare,
so a class may add its own and compose behaviors over them.

```ts
@versioned @agents("definitions") export abstract class Agent {}

@agents("runs")
@references<AgentRun>({})
export abstract class AgentRun
  implements
    Lease<AgentRun, { onExpiry: { transition: "failed"; from: ["running", "waiting"] } }>,
    Retries<AgentRun, { classes: { sandbox: { attempts: 2; backoff: { firstMs: 1000; maxMs: 60000 } } }; totalAttempts: 3; exhaustedState: "failed" }>
{
  @key id: AutoGenerate<Identity.UUID>;
  status: Workflow<{ states: ["queued", "running", "waiting", "done", "failed", "cancelled"]; transitions: [...] }>;
}

@queue() @agents("requests") export abstract class AgentRunRequest {}
@agents("policies") export abstract class AutonomyPolicy {}
@agents("approvals") @event<AgentApproval>({ kind: "approval.requested", urgency: "urgent" })
export abstract class AgentApproval {}
@agents("controls") export abstract class AgentControl implements Budget<AgentControl, { ... }> { ... }
```

| Part | What a row is |
| --- | --- |
| `definitions` | An agent: name, purpose, instructions, the role it acts with, tools, credential references, model, provider slots, limits. `@versioned`, so a run records the version it ran. |
| `runs` | A run: its trigger, definition version, scope, reads, writes, messages, approvals, cost, outcome, credential uses and transcript. |
| `requests` | A message of the dispatcher's queue: a run request, a watch of a running instance, an ask's deadline. |
| `policies` | `auto`, `tell` or `ask`, per agent (or every agent) and per operation or class. |
| `approvals` | What an ask holds: the operation, the exact parameters and their hash, who may decide, and who did on which channel. |
| `controls` | One row per agent and one for every agent: the stop switch, and a budget limit a Budget meter reads. |

Every part is required, and the orchestration runs on Postgres. An API
over such a database uses the `agents` runtime feature, so an environment
that sets no sandbox slot or no model slot is refused.

## Start a run

A run request is a message of the requests queue. `agents.request(tx, {...})`
puts one in the caller's transaction.

| Trigger | How it arrives |
| --- | --- |
| `route` | A notifier route whose recipients name `{ agent: "reviewer" }`. The run is about the event's record. |
| `assigned` | The app assigns a row and requests a run about it. A paused agent's row goes back to its queue (`onReturn`). |
| `schedule` | A Schedules run or a Recurrence event, through a route or the app's Workers method. |
| `principal` | A person asks, naming whom the run acts for. |
| `inbound` | A party's message (D157): `{ trigger: "inbound", party, message, grant }`. It names no agent; the definition whose `inbound` is set takes it, and its scope is the grant's target. |

A run's principal may not request a run: only the dispatcher starts one.

The dispatcher is a `@worker` of the requests queue. It claims a request,
writes the run, takes the run's Lease, mints the token and starts a
sandbox instance with the definition, the token, the agent routes' URL and
the credential references. A watch message polls the instance until it
ends; then the dispatcher revokes the token, meters the instance's cost,
writes the transcript and records the outcome. On Workers the queue drains
at each Cron firing, and a message due within the minute wakes it.

## The run's token

The token is a grant (D156) per target row, held by the run, whose role is
the agent's. The agent routes' authenticator resolves it to a principal of
kind `agent`:

- its permissions are the role's, narrowed to those whom it acts for holds;
- it reaches its target rows and the rows its role's `reaches` name;
- each row's audience and agent access (`followRole`, `readOnly`, `none`)
  holds, as for any agent run.

The token ends when the run does. A tool runs as this principal, so no
tool widens the run's scope. Search across tables, through its `search`
tool, finds only rows within it.

## Autonomy

Each tool call passes the run's policy. The most specific row wins: the
agent and the operation, then the agent and the class, then every agent.
With no row, the operation's own mode holds, then its class's default.

| Class | Default |
| --- | --- |
| `read` | `auto` |
| `internalWrite` | `auto` |
| `messageParty` | `ask` |
| `moneyMovement` | `ask`, and nothing else |

An API operation says its class with `@autonomy("moneyMovement")`. The
build refuses `@autonomy("moneyMovement", "auto")`, and `setPolicy`
refuses a row that lets money move on its own.

`ask` writes an approval with the exact parameters and their SHA-256, and
raises `approval.requested` on it as urgent. A route sends it to the
people who hold its permission (`agents.approve` unless the row says). The
call answers `428 approval_required`, and the sandbox tries it again.
"approve 12" on any channel, or `agents.approve`, decides it; the same
call then runs once. A call with any other parameter finds no approval of
its hash, supersedes the open ones, and asks again.

## Budgets and stop switches

Each turn's usage counts on the agent's control row, whose Budget meter's
scope is every agent's budget. At the row's `warnAt` percent the
dispatcher raises `agent.budgetWarning`. At the agent's cap the agent
pauses and its runs are cancelled. At every agent's cap a scheduled run is
refused, and a person's run waits for an `agents.overBudget` approval.

`agents.pause(principal, agent)` pauses one agent; `pause(principal, null)`
pauses every one. Each run in flight is cancelled through its sandbox
provider and its token revoked.

## Credentials and outside content

A definition names credential references, never values. The sandbox
instance resolves each with `useCredential` into the request or the field
that needs it, and tells the agent routes the reference, the provider, the
target and the time. The run records that; no value reaches the run row,
the transcript, a log or the model.

A tool marked `untrusted`, such as a fetch of a web page, and a party's
inbound message reach the model marked untrusted, and the run counts them.

## Administer agents

| Permission | What it allows |
| --- | --- |
| `agents.define` | Define and edit agents |
| `agents.credentials` | Change an agent's credential references |
| `agents.autonomy` | Write autonomy policies |
| `agents.budgets` | Set budgets; approve a run past every agent's cap |
| `agents.pause` | Pause and resume |
| `agents.approve` | Decide an approval, unless its policy names another |

A transcript is kept 30 days after its run ends (`transcriptRetentionDays`);
`prune` deletes it from the bucket. The run row stays.
