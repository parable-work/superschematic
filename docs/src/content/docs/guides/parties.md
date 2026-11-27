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
| `@party` on a table; the conversations' tables and the run requests queue, which the build fills | SQLite and D1 |
| one inbound path: a user's reply, a granted party's run request, a party's timeline, triage | a generated server that mounts the path and its workers |
| email held to its SPF, DKIM and DMARC results; one inbound address per deployment | Comments' `via`, the channel a message came on |
| threads by `Message-ID`, `In-Reply-To` and `References`, and parties that share an address | giving a held message to a party, its address added from triage; a triage page in Topcoat |
| messages as rows, with their files and the rows they are about | |
| a lapsed identity, verified again by a code | |
| sending as a permission, with a rate limit, an approval policy and opt-outs | |
| a message's trust narrowing the agent run that takes its request: a sender entered by hand asks before every write ([agent runs](/superschematic/guides/agent-runs/)) | |
| the Go runtime's `inbound` package, on Postgres | |
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
`inbound.triage(principal)` and `inbound.settle(principal, message)`.
`inbound.timeline(principal, party)` lists a party's messages on every
channel.

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
repositories write a key. A Go server over a database that lists `sqlite`
is refused the parties' tables, as the loader refuses them for every
language.

## In Rust

The Rust HTTP runtime's `orm::parties` and `orm::inbound` are the same
path, on Postgres (D193). The Postgres definition of a generated Rust
types crate carries each table's `@party` and its conversations part, so
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
repositories write a key. A Rust types crate that lists `sqlite` is
refused `@party`, the conversations and the run requests, as the loader
refuses them for every language.
