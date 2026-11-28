---
title: Outside parties and inbound messages
description: Mark a table's rows as parties who act through grants, take every text and email down one path that decides who sent it, keep conversations as rows, and send to a party as a permission.
sidebar:
  order: 5.7
---

Not everyone an app deals with signs in. A contractor texts a photo of a
finished job; a supplier answers an email. A party is a row of an app's
own table that is also a principal: it acts only through the grants it
holds ([per-record access](/superschematic/guides/api-routes/#per-record-access)), and it is
recognised by the channel identities that resolve to it
([events and notifications](/superschematic/guides/notifications/)). Every inbound message
takes one path, which decides who sent it and what it may reach.

| Built | Not built yet |
| --- | --- |
| `@party` on a table; the conversations' tables and the run requests queue, which the build fills, on Postgres, SQLite and D1 | Rust and Go on D1 |
| one inbound path: a user's reply, a granted party's run request, a party's timeline, triage; a generated TypeScript server that builds it into its `Deps` and mounts each relay's inbound route ([below](#take-what-comes-in)) | the same in a generated Go or Rust server |
| email held to its SPF, DKIM and DMARC results; one inbound address per deployment | Comments' `via`, the channel a message came on |
| threads by `Message-ID`, `In-Reply-To` and `References`, and parties that share an address | |
| a held message settled onto a party from triage, its address entered by hand or verified by a code (TypeScript, [below](#settle-a-held-message-onto-a-party)) | the same in Go and Rust |
| the triage as generated routes, SDK methods and MCP tools (`@triageOperations`, [below](#serve-triage-as-routes)), run by a generated TypeScript server, and a Topcoat console's pages over them | the routes' handlers in a generated Go or Rust server, which keep them the implementation's |
| a messaging bridge's inbound, with a key that can only relay messages (`examples/runtime-providers`'s `chat-bridge`) | the bridge as a Go or Rust provider module |
| messages as rows, with their files and the rows they are about | |
| a lapsed identity, verified again by a code | |
| sending as a permission, with a rate limit, an approval policy and opt-outs | |
| a message's trust narrowing the agent run that takes its request: a sender entered by hand asks before every write ([agent runs](/superschematic/guides/agent-runs/)) | |
| the Go runtime's `inbound` package, on Postgres, which a generated Go server builds into its `Deps` with its relay callbacks and its run requests' worker | |
| the Rust runtime's `orm::parties` and `orm::inbound`, on Postgres | |

[D157](https://github.com/parable-work/superschematic/issues/606)
records the design and what is left. `examples/parties` is the worked
example this page quotes.

## Declare the tables

`@party` marks a table whose rows are parties. Its key is a UUID, which a
grant names as its holder and a channel identity as its principal. The
conversations are the core's tables, as the channel identities are: empty
classes the build fills. The messages class may take `@references` and
`@attachments`, so a message holds its files and points at the rows it is
about. `@runRequests` marks the queue a granted party's message puts a run
request on.

```ts
@party<Contact>({ title: "name" })
export abstract class Contact {
  @key id: AutoGenerate<Identity.UUID>;
  name: string;
}

@conversations("conversations")
export abstract class Conversation {}

@conversations("messages")
@references<Message>({ cleared: ReferenceCleared })
@attachments<Message>({ released: AttachmentReleased })
export abstract class Message {}

@queue() @runRequests()
export abstract class RunRequest {}
```

A schema with a `@party` table declares the grants table; one with the
conversations declares the channel identities. Both run on Postgres.

## Take what comes in

A relay's `@webhook` route hands its request to the inbound path:

```ts
const inbound = createInbound({ orm, notifier, runs: runRequestQueue, providers, bucket, inboundAddress: "inbox@example.test" });
app.post("/webhooks/sms/inbound", async c =>
  c.json(await inbound.inbound("SMS", { headers: Object.fromEntries(c.req.raw.headers), body: await c.req.text() })));
```

A generated TypeScript server of an API over a Postgres database with the
conversations and a run requests queue does this itself: `deps.inbound` is
`createInbound` over `deps.orm`, `deps.notifier` and the queue, with the
channel slots of `deps.providers`, and `POST /webhooks/<slot>/inbound`
hands each relay's request to it. The bucket, the inbound address and the
approval policy of a send are the implementation's `createRuntimeOptions`'
(`inbound: { bucket: deps.files, inboundAddress, approve }`); with the
agent orchestration the approval policy is the dispatcher's
`sendApproval`, which the server wires. A `@worker` of the run requests
is the dispatcher's where the database uses the orchestration, and the
implementation's otherwise ([act on a run request](#act-on-a-run-request)).

The adapter reads the request, fetches a text's photos and reports an
email's authentication. The core decides who sent it:

| Sender | What happens |
| --- | --- |
| A user's verified address | A reply, which the notifier runs as the user |
| A party with an active grant | A message row with its files, and a run request `{ trigger: "inbound", party, message, grant }` |
| A party with no active grant | A message on the party's timeline, held in triage |
| A party whose grants all ended since its address was verified | Held in triage as an unknown sender's, until the address is verified again |
| An unknown address, or an email that fails its authentication | Held in triage |

An email is trusted when its adapter reports DMARC pass and neither SPF
nor DKIM fails; one that reports nothing is no one's. Mail to another
address than the deployment's inbound address is dropped. Two parties may
share an address. A message from it belongs to the party whose
conversation it continues, by its `In-Reply-To` or `References`, or the id
of the text it answers. A message that names neither party is held in
triage.

The office reads triage and settles it with `parties.triage`:
`inbound.triage(principal, { limit?, offset?, channel? })`,
`inbound.attachmentsOf(principal, message)` and
`inbound.settle(principal, message)`, or the generated routes
([below](#serve-triage-as-routes)).
`inbound.timeline(principal, party)` lists a party's messages on every
channel.

### Settle a held message onto a party

A message from an address no one holds can be a party's: a contractor
texting from a new phone. `settleOnto` adds the sender's address as the
party's identity, and files the message as if it had come from the party
(#606):

```ts
await inbound.settleOnto(staff, message, { party: { table: "Contact", key }, trust: "entered" });
```

| `trust` | What happens |
| --- | --- |
| `entered` | The address is the party's at once, entered by hand: the lowest trust, so a run on its messages asks before every write. The message is filed now. |
| `verified` | A code goes to the address, and the message waits (`verifying`). When the address answers it, the message is filed at the trust of a verified address. |

Filed means as the path files a party's message: the party's
conversation on the channel, a reference to the party's row, and with an
active grant a run request and triage closed; with none, the party's
timeline and triage still open. The settler holds `parties.triage` and
reads the party's row, as a sender does. An address that is a user's is
refused: a user shares an address with no one.

### Serve triage as routes

An API serves the triage to its staff from a class with no members
marked `@triageOperations`, naming the `@party` table a held message is
settled onto (#746). `refersTo` names the API's type its listing
operation lists the party rows as, so a UI offers them to pick from:

```ts
@triageOperations(Contact, { refersTo: ContactView })
@requirePermission(["parties.triage"])
export abstract class Triage {}
```

The build adds four operations, an SDK method each, and an MCP tool for
the two reads:

| Operation | Route (below `/api`) | What it does |
| --- | --- | --- |
| `triage` | `GET triage` | The held messages, newest first, 20 a page (at most 100); `channel` keeps one kind of channel (`sms`, `email`, `chat`). |
| `triageMessage` | `GET triage/{message}` | One message that was held, open or settled: its sender's address, channel, body, files and what became of it. |
| `settleOnto` | `POST triage/{message}/settle` | `settleOnto` with `{ party, trust }`; answers `run`, `timeline` or `verifying`. |
| `dismiss` | `POST triage/{message}/dismiss` | Closes the message's triage: it stays on its conversation, settled onto no one. |

Each route needs the class's `@requirePermission`, `parties.triage` when
it names none, and the inbound path checks its own triage permission
again; `@publicRoute` on the class is refused. A message never held is
404, a settle or dismiss of one no longer held is 409, a party the caller
cannot read is 404, and an address that is a user's is 422. The settle
and the dismiss are no tools: each decides who a sender is, which is a
person's call.

A generated TypeScript server runs them through `Deps.inbound`
(`RouterOptions.inbound`), with no implementation method. A Go or Rust
server keeps each a method of the implementation, which calls its
runtime's inbound path.

A [Topcoat console](/superschematic/guides/topcoat/#generated-pages)
serves them as a resource of its own, the held messages at
`/console/triage`: the list, paged and filtered by channel; a message's
page, with its files and the settle and dismiss forms, the settle's party
a picker of the `refersTo` type's rows and its trust a select. The nav
offers the list only to a caller who holds the triage permission.

### A messaging bridge

A messaging network's inbound side can be a bridge: a process that
connects outward, to the network and to the app, so its host needs no
open port. `examples/runtime-providers`'s `chat-bridge` is one. The app
sends through the network's API. The bridge's loop (`runBridge`) posts
each message the network received to `POST /webhooks/<slot>/inbound`
with the slot's relay key, its `CALLER_KEY` (#744), which the adapter
checks. The generated server resolves that key to the provider's own
principal (`extensionAuthenticator`, outermost), whose role is
`messages.relay`, so it can relay messages and nothing else. A sender is a handle, which a channel identity of kind `handle`
names, and its message takes the same path as a text's.

## Act on a run request

The run request names the party, the message and the grant. The agent
dispatcher takes the queue as it is
([agent runs](/superschematic/guides/agent-runs/#start-a-run)), or an
app's own handler does:

```ts
const queue = inbound.runWorker(pool, async request => {
  const party = await inbound.principalOf(request.party, { grant: request.grant });
  const grant = party.grants?.[0];
  if (!grant) return; // the grant ended since the message came
  const message = await inbound.message(request.message);
  const report = await orm.as(party).report.createOne({ jobId: grant.targetId, body: String(message!.body) });
  await inbound.file({ message: request.message, to: { table: "Report", key: String(report.id) }, as: party });
});
```

`principalOf` resolves the party as a grant's token resolves: kind
`party`, the grant's reach and its role's permissions. The report is
written as the party, so it lands only where the grant's role reaches.
`file` points the row at the message, so the row lists it among its
backlinks, and files the message's photos onto it with no second upload.
A row of a table with no `@attachments` gets the reference alone, and so
does every row when the messages table has no `@attachments`. Filing is
idempotent: a run request whose handler failed after `file` runs again,
and filing the message again adds no second reference or file.

## Send to a party

```ts
await inbound.send({ as: principal, party: { table: "Contact", key }, channel: "sms", body: "Booked for Monday." });
```

Sending takes `parties.message` (or the `sendPermission` given), and the
sender must read the party's row. The party's identity on the channel must
be verified or entered by hand, not opted out and not lapsed. A party is
sent at most `rateLimit` messages in its window, 20 an hour unless set.
The `approve` policy answers `allow`, `deny` or `ask`, and
`agents.sendApproval` holds a run's sends to its autonomy
([agent runs](/superschematic/guides/agent-runs/#autonomy)); a message asked
for waits as `awaitingApproval` until `release` sends it or `reject`
refuses it. A message records its author, kind and on whose behalf it was
sent (`forSubject`), the channel (`via`), and the rows it is `about`.

An email carries a `Message-ID` and asks for its reply at the inbound
address, so the answer threads. A text of STOP is the adapter's to read:
the core records it on every identity of the address and sends nothing to
it until START.

## On SQLite and D1

A database that lists `sqlite` keeps the parties, the conversations and
the run requests queue as Postgres does (D189). `createInbound` comes from
the runtime's `./d1` entry too, over the `./orm/sqlite` entry's
repositories, and `inbound.runWorker(db, handle)` claims the run requests
over a `SqlDatabase`, a D1 binding or `openSqlite`'s file, as over a pg
pool. A message, its files and its run request are written as the
repositories write them, each statement its own on D1, which holds no
transaction open: a run request is enqueued once the message and its
files are stored.

## In Go

The Go HTTP runtime's `inbound` package is the same path, on Postgres
(D192). The generated Go ORM of a database with a `@party` table, the
conversations or a `@runRequests` queue, and the notifier, writes
`orchestration.go`: the conversations' tables by part with their columns,
the `@party` tables, the run requests queue, and
`Database.Orchestration()`, the notifier's host with what the inbound path
adds. `inbound.New` finds the conversations, the messages and the channel
identities from it:

```go
host := db.Orchestration()
notifier, err := notify.New(host, notify.Options{Providers: deps.Providers})
in, err := inbound.New(host, inbound.Options{
	Notifier:       notifier,
	Providers:      deps.Providers,
	Bucket:         inbound.BucketOf(files),
	InboundAddress: "inbox@example.test",
})

// A relay's @webhook route.
outcomes, err := in.Inbound(ctx, "SMS", request)
// The run requests queue's worker: each request to a handler, the agent
// dispatcher's or the app's own.
queue, err := in.RunQueue(dispatcher.HandleRunRequest)
err = worker.Run(ctx, queue, options)
// A send, held to the permission, the identity, the rate limit and the
// approval policy.
sent, err := in.Send(ctx, inbound.SendInput{As: principal, Party: orchestration.PartyRef{Table: "Contact", Key: key}, Channel: "sms", Body: "Booked for Monday."})
```

`PrincipalOf` is `orchestration.PartyPrincipal`, a party with its grants'
reach and their roles' permissions; Go's `access.Principal` holds no
permissions, so `orchestration.Principal` carries them beside it, and a
handler reads and writes as the party with `orm.WithPrincipal`.
`orchestration.IdentityLapsed` decides a lapsed identity from the rows,
which the notifier now asks too before it sends to a party's address. The
conversations' tables are read and written by statements over the columns
the definitions name, as the notifier's are; a party's row and a row a
message is filed onto are read through their repository as the caller.
The approval policy is a `SendApproval`: the app's (`ApprovalFunc`), or
the agent dispatcher, which is built before the path and given it
(`SetInbound`). A run request's message and grant are in base62, as the
repositories write a key.

On SQLite the Go runtime runs the same inbound path (#618), as the TypeScript one
does (D189). `(*SQLiteDatabase).Orchestration()` is its host over the SQLite file,
and the runtime writes its statements over its own tables in SQLite's
words: a parameter `?n`, a list read through `json_each`, a time as RFC
3339 text, each cell read back as the Postgres driver reads its column
([Go on SQLite](/superschematic/guides/database-tables/#go-on-sqlite)). A message's
files go through the SQLite database's attachments, and `RunQueue` claims
the run requests on SQLite.

A stack's Go server builds the path into `Deps.Inbound` itself, after the
notifier and with the dispatcher as its send approval when the database
has agents, and the bucket, the inbound address and the rest its
implementation's `NewWiring` answers in `Wiring.Inbound`. A relay's
callback at `POST /webhooks/<slot>/inbound` takes the path, which hands a
user's reply to the notifier, and a `@worker` of the `@runRequests` queue
hands each request to the dispatcher, with no method of the
implementation's. [The generated Go server](/superschematic/guides/agent-runs/#the-generated-go-server)
lists the whole wiring.

## In Rust

The Rust HTTP runtime's `orm::parties` and `orm::inbound` are the same
path, on Postgres (D193) and on a SQLite file (#618). Each definition of
a generated Rust types crate carries each table's `@party` and its
conversations part, so
`Inbound::new` finds the conversations, the messages and the channel
identities by itself, and the crate re-exports both modules. It takes the
database's `Tables`, the notifier (D191) and the `@runRequests` queue's
descriptor:

```rust
use superschematic_http_runtime::orm::inbound::{Inbound, SendInput};
use superschematic_http_runtime::orm::parties::PartyRef;

let inbound = Inbound::new(orm::postgres::tables(pool.clone()), notifier.clone(), QueueDefinition::parse(queues::RUN_REQUEST_QUEUE)?)?
    .with_providers(deps.providers.clone())
    .with_bucket(deps.files.clone())
    .with_inbound_address("inbox@example.test");

// A relay's @webhook route.
let outcomes = inbound.inbound("SMS", &request).await?;
// The run requests queue's worker: each request to a handler, the agent
// dispatcher's or the app's own.
run_worker(inbound.run_queue(), move |request| { let agents = agents.clone(); async move { Ok(agents.handle(request.into()).await?) } }, options, shutdown).await?;
// A send, held to the permission, the identity, the rate limit and the
// approval policy.
inbound.send(SendInput::new(principal, PartyRef::new("Contact", key), "sms", "Booked for Monday.")).await?;
```

`principal_of` is `party_principal`, a party with its grants' reach and
their roles' permissions, and `identity_lapsed` decides a lapsed identity
from the rows, which the notifier now asks too before it sends to a
party's address. Rust has no ambient caller, so a read as the sender or
the party opens the repositories for it (`with_access`). The approval
policy is a `SendApproval`: the app's, or the agent dispatcher's
`send_approval`. A run request's message and grant are in base62, as the
repositories write a key. On SQLite `Inbound::new` takes
`orm::sqlite::tables(database)`, the path's transactions are held writes
of the file ([in Rust](/superschematic/guides/notifications/#in-rust)), a
message's files go through the SQLite attachments, and `run_queue()`
claims the run requests on the file. `party_principal` and
`identity_lapsed` take an `orm::store::Db` of either dialect. A generated
Rust server over a database that lists `sqlite` first wires the path over
the file, and its run requests' worker claims there (#725).
