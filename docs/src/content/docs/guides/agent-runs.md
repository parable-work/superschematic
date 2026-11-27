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
| `@agents("<part>")` on the orchestration's tables, which the build fills, on Postgres, SQLite and D1 | Go and Rust on SQLite and D1 |
| run requests on a queue from routes, assignments, schedules, principals and inbound messages | a real container platform's sandbox provider and a model API's provider |
| the run's token as a grant per target row, acting for its principal, its paths narrowed to a party's grant | a live run on Workers (`smoke.sh Cloudflare` is the maintainer's) |
| autonomy per agent and operation or class, approvals bound to their parameters' hash, `@autonomy` in the generated MCP tools | |
| an inbound message's trust: a sender entered by hand asks before every write | |
| budgets per agent and in total, warnings, pauses; stop switches | |
| asking in a row's thread, with a follow-up row at the deadline | |
| redacted transcripts in a bucket; credentials that stay in the sandbox | |
| a generated TypeScript server that builds the dispatcher into its `Deps`, mounts its routes and runs its queues | the same over a database that lists `sqlite` first, whose generated dispatcher is built from the `./postgres` entry; a generated Go server that builds the dispatcher into its `Deps`, and a Rust server that does the same; `@autonomy` in a Go server, which serves no MCP endpoint |
| the Go runtime's `agents` package and `providers.RunAgentInstance`, on Postgres | |
| the Rust runtime's `orm::agents` and `run_agent_instance`, on Postgres | |

[D158](https://github.com/parable-work/superschematic/issues/607) and [D183](https://github.com/parable-work/superschematic/issues/612)
record the design and what is left. `examples/agent-orchestration` is
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
that sets no sandbox slot or no model slot is refused
([runtime providers](/superschematic/guides/runtime-providers/#features-that-need-a-provider)).

## Start a run

A run request is a message of the requests queue. `agents.request(tx, {...})`
puts one in the caller's transaction.

| Trigger | How it arrives |
| --- | --- |
| `route` | A notifier route whose recipients name `{ agent: "reviewer" }` ([route it](/superschematic/guides/notifications/#route-it)). The run is about the event's record. |
| `assigned` | The app assigns a row and requests a run about it. A paused agent's row goes back to its queue (`onReturn`). |
| `schedule` | A Schedules run or a Recurrence event, through a route or the app's Workers method. |
| `principal` | A person asks, naming whom the run acts for. |
| `inbound` | A party's message ([outside parties](/superschematic/guides/parties/#act-on-a-run-request), D157): `createInbound` puts `{ trigger: "inbound", party, message, grant }` on the `@runRequests` queue, and `inbound.runWorker(pool, m => agents.handle(m))` hands it over as it is. It names no agent; the definition whose `inbound` is set takes it. Its scope is the grant's target, the party's row and the message's. |

A run's principal may not request a run: only the dispatcher starts one.
An agent that hands work on asks a principal, whose request starts the
next run.

The dispatcher is a `@worker` of the requests queue. A generated
TypeScript server builds it into the API's `Deps` (`deps.agents`) when the
API's database declares the orchestration; see [Wire the
dispatcher](#wire-the-dispatcher). It claims a request,
writes the run, takes the run's Lease, mints the token and starts a
sandbox instance with the definition, the token, the agent routes' URL and
the credential references. A watch message polls the instance until it
ends; then the dispatcher revokes the token, meters the instance's cost,
writes the transcript and records the outcome. On Workers the queue drains
at each Cron firing, and a message due within the minute wakes it.

## The run's token

The token is a grant (D156,
[per-record access](/superschematic/guides/api-routes/#per-record-access))
per target row, held by the run, whose role is the agent's. The agent
routes' authenticator resolves it to a principal of kind `agent`:

- its permissions are the role's, narrowed to those whom it acts for holds;
- it reaches its target rows and the rows its role's `reaches` name;
- acting for a party, it reaches through only the paths the party's grant
  of the same target reaches too: the role's `TaskNote:task` reaches a
  task's notes only when the party's grant's role names it as well, and
  from the party's row and its message's it reaches those rows alone;
- each row's audience and agent access (`followRole`, `readOnly`, `none`)
  holds, as for any agent run.

A run an inbound message started records the message's trust on the run
(`trust`) and in the token's claims. `verified` is a message from an
identity a code verified; `entered` is one from an identity entered by
hand, and so is a message the dispatcher cannot read. An `entered`
sender's run reads as its policy says, and every other operation asks,
whatever a policy row says, as a money movement always does.

The token ends when the run does. A tool runs as this principal, so no
tool widens the run's scope. Search across tables, through its `search`
tool, finds only rows within it.

Only the dispatcher resolves a run's token. `grantAuthenticator`, the
authenticator of an app's parties, answers it 401: as its grant's holder
the run would hold the role's every permission, narrowed to no one and
held to no autonomy. An app that serves both parties and runs mounts
`agents.authenticator(inner)` alone, which resolves a running run's token
as the run, any other grant's as its holder, an ended run's as a 401, and
hands every other request to `inner`:

```ts
buildRouter(implementations, {
  orm,
  authenticate: agents.authenticator(authenticate),
  accessLog: accessLogOf(orm),
});
```

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
refuses a row that lets money move on its own. The generated MCP tool
records carry each operation's `@autonomy`, so `agents.mcpGate`, the
endpoint's call gate, holds a run's call to the operation's class; an
operation with none is an internal write.

`ask` writes an approval with the exact parameters and their SHA-256, and
raises `approval.requested` on it as urgent. A route sends it to the
people who hold its permission (`agents.approve` unless the row says). The
call answers `428 approval_required`, and the sandbox tries it again.
"approve 12" on any channel, or `agents.approve`, decides it; the same
call then runs once. A call with any other parameter finds no approval of
its hash, supersedes the open ones, and asks again.

A message to a party is class `messageParty`, so it asks by default. With
`createAgents({ inbound })`, a run writes to a party with the
`message_party` tool, which sends through D157's `inbound.send`. Pass
`approve: request => agents.sendApproval(request)` to `createInbound`: a
run's send that asks is held `awaitingApproval`, the approval holds its
message, and approving it releases the message; denying it refuses it.

## Wire the dispatcher

A TypeScript API whose database declares the orchestration holds the
dispatcher in its `Deps`. The generated entrypoint builds `deps.agents`
with `createAgents` over `deps.orm` and the requests queue, and the rest of
its options from the implementation's `createAgentOptions`:

```ts
export const createAgentOptions: AgentOptionsConstructor = deps => ({
  endpoint: deps.config.AGENT_ENDPOINT ?? 'http://localhost:8080/api/agents',
  providers, // providersFrom over the config's slots (D159)
  notifier, // createNotifier, whose approval replies run agents' reply actions
  inbound, // createInbound, whose approve hook is agents.sendApproval
  tools,
  search: true,
});
```

| What the generated server does | Where |
| --- | --- |
| builds `deps.agents` with `withAgents` | every entrypoint that builds `Deps` |
| mounts `deps.agents.routes()` at the endpoint's path | the server |
| passes `agents.mcpGate` as the MCP endpoint's call gate | a server that serves the API's MCP endpoint |
| hands each message to `agents.handle` | a `@worker` of the requests queue, and of the `@runRequests` queue (D157) |
| prunes the transcripts past their retention with the queue | the requests queue's worker |

The implementation's `Workers` has no method for a worker of either
queue. An authenticator that takes a run's token on the API's own routes
is `deps.agents.authenticator(inner)`; such a route holds an agent's call
to its run's autonomy with `deps.agents.gate`, as `examples/agent-orchestration`'s
`payInvoice` does.

## Budgets and stop switches

Each turn's usage counts on the agent's control row, whose Budget meter's
scope is every agent's budget. At the row's `warnAt` percent the
dispatcher raises `agent.budgetWarning`. At the agent's cap the agent
pauses and its runs are cancelled. At every agent's cap a scheduled run is
refused, and a person's run waits for an `agents.overBudget` approval.

`agents.pause(principal, agent)` pauses one agent; `pause(principal, null)`
pauses every one. Each run in flight is cancelled through its sandbox
provider and its token revoked.

## Each turn

The sandbox instance posts each turn's tool results to the agent routes'
`turn`, and the dispatcher completes the turn with the agent's model
provider over the run's whole transcript: the task, each earlier
completion as an assistant message with the tool calls it made
(`toolCalls`), and each tool result after the call it answers. A provider
that keeps its own record of a turn, such as Claude's thinking blocks and
their signatures, returns it as the completion's `providerState`. The
dispatcher keeps it with the completion and passes it back on that
assistant message, unchanged, in every later turn, so the provider can
rebuild its API's conversation as that API requires. It is the one part of
the transcript kept unredacted while the run lives; a read of the
transcript, and the transcript in the bucket, hold it redacted.

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

## On SQLite and D1

A database that lists `sqlite` keeps the orchestration's tables as
Postgres does (D189). The definitions are `@versioned`, and SQLite keeps
their history by row triggers, so a run's `definitionVersion` names a
version the history holds. The runs' Lease and Retries and the controls'
Budget run through the behaviors' core in the repository's transaction,
as on SQLite every behavior does (D186). `createAgents` comes from the
runtime's `./d1` entry too, over the `./orm/sqlite` entry's repositories,
and `agents.workerQueue(db)` claims its requests over a `SqlDatabase`.

A generated server builds its dispatcher from the runtime's `./postgres`
entry, and its workers claim through the Postgres queue client, so an API
whose database lists `sqlite` first and declares the orchestration is
refused its generated dispatcher, though its `Deps.orm` is the
`./orm/sqlite` entry's: the app builds `createAgents` over
`createOrm(db)` of the `./orm/sqlite` entry itself.

## In Go

The Go HTTP runtime's `agents` package is the same dispatcher, on
Postgres (D192), with D183's trust and narrowed paths. The generated Go
ORM of a database with the `@agents` tables and the notifier writes
`orchestration.go`: the orchestration's tables by part with their columns,
each table's behaviors' uses, and the requests queue, and
`Database.Orchestration()`, the host the dispatcher runs through. A table
that composes behaviors (the runs' Lease, Retries and Workflow, the
controls' Budget) or is `@versioned` (the definitions) is written through
its repository, and its behaviors' operations run through the core; the
policies and approvals by statements over their columns. The dispatcher
is built first, then given the notifier and the inbound path, which take
its reply actions, its requests and its send approval:

```go
host := db.Orchestration()
dispatcher, err := agents.New(host, agents.Options{
	Endpoint:    "https://app.example.test/api/agents",
	Providers:   deps.Providers,
	Tools:       tools,
	Search:      &crosstable.SearchOptions{},
	Transcripts: transcripts,
})
notifier, err := notify.New(host, notify.Options{Providers: deps.Providers, Agents: dispatcher, Replies: map[string]map[string]notify.ReplyAction{"approval.requested": dispatcher.ReplyActions()}})
dispatcher.SetNotifier(notifier)
in, err := inbound.New(host, inbound.Options{Notifier: notifier, Providers: deps.Providers, Approve: dispatcher})
dispatcher.SetInbound(in)

// The agent routes, under the endpoint's path.
mux.Handle("/api/agents/", dispatcher.Routes(agents.RoutesOptions{}))
// The requests queue's worker, and the run requests queue's.
runs, err := in.RunQueue(dispatcher.HandleRunRequest)
err = worker.RunAll(ctx, []worker.Queue{dispatcher.WorkerQueue(), runs}, options)
```

A tool of the app is an `agents.Tool`: its class, its mode, the
permission it needs, and its `Run`, which a `ToolContext` is given, a
context that acts as the run's principal (`Ctx`) in a transaction of the
database (`Tx`, the ORM's `TxInterface`). The run's principal
(`PrincipalOf`) carries the run, the agent, the definition's version and
the trust beside the permissions, as Go's `access.Principal` holds none of
them. Each call to the agent routes runs its work to its end whether or
not the instance stays, so a run a call cancels is not left half done. A
run's token on the app's own routes goes through `Authenticator`, in place
of `session.GrantAuthenticator`, which refuses one: the route runs as the
run's principal, narrowed as above, and each call is held to the run's
autonomy first, so an entered sender's write answers 428 until someone
approves it. The
sandbox's side is `providers.RunAgentInstance`. A Go server is refused an
operation's `@autonomy`, which holds an agent run's calls of the API's MCP
tools, since it serves no MCP endpoint; `MCPGate` is that gate for the day
one does.

## In Rust

The Rust HTTP runtime's `orm::agents` is the same dispatcher, on Postgres
(D193). The Postgres definition of a generated Rust types crate carries
each table's `@agents` part, so `Agents::new` finds the definitions, the
runs, the policies, the approvals and the controls by itself, and the
crate re-exports the module. The notifier takes the dispatcher's reply
actions and its requests queue, so it is built after the dispatcher, and
the dispatcher is given the notifier and the inbound path once they are:

```rust
use superschematic_http_runtime::orm::agents::{AgentTool, Agents, OperationClass};

let requests = QueueDefinition::parse(queues::AGENT_RUN_REQUEST_QUEUE)?;
let agents = Agents::new(orm::postgres::tables(pool.clone()), requests.clone(), "http://localhost:8080/api/agents")?
    .with_providers(deps.providers.clone())
    .with_tool("read_task", AgentTool::new("Reads a task.", schema, OperationClass::Read, Arc::new(ReadTask)))
    .with_search(SearchOptions::default());
let [(approve, approve_action), (deny, deny_action)] = agents.reply_actions();
let notifier = Notifier::new(orm::postgres::tables(pool.clone()), dispatch)?
    .with_reply("approval.requested", approve, approve_action)
    .with_reply("approval.requested", deny, deny_action)
    .with_agents(requests);
let inbound = Inbound::new(orm::postgres::tables(pool.clone()), notifier.clone(), runs)?
    .with_approval(agents.send_approval());
agents.set_notifier(notifier.clone());
agents.set_inbound(inbound.clone());

// The agent routes, ahead of the API's router, and the MCP endpoint's gate.
let router = agents.routes(Some(authenticator.clone())).merge(api_router);
let mcp = EndpointOptions::default().with_call_gate(agents.mcp_gate());
// The requests queue's worker.
run_worker(agents.worker_queue(), move |message| { let agents = agents.clone(); async move { Ok(agents.handle(message).await?) } }, options, shutdown).await?;
```

A tool of the app is an `AgentToolRun`, given the repositories of a
transaction that act as the run's principal. A run's token resolves
through `principal_of`, or `authenticator(inner)` on the API's own routes.
The MCP tool records of a generated Rust router carry each operation's
`@autonomy`, which `mcp_gate` holds an agent run's call to once the app
passes it in the router's MCP endpoint options; the generated Rust server
does not build the dispatcher into its `Deps` yet. The sandbox's side is
`providers::run_agent_instance`, under the runtime's `http-client`
feature. The agent routes run each call's work in a task of its own, so a
run cancelled while its turn runs, by a pause or a budget's cap, does not
leave the work half done when its connection closes. A Rust types crate
that lists `sqlite` is refused the orchestration, which runs on SQLite and
D1 in TypeScript alone ([On SQLite and D1](#on-sqlite-and-d1)).
