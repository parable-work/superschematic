---
title: Events and notifications
description: Declare the events a table's rows raise, route them to people with rows, send each through the channels its recipient prefers, and take replies by text or email as the sender's own input.
sidebar:
  order: 5.69
---

A record changes, and someone should hear of it: an assigned ticket, a
duty missed, a file that went away. The notifier is the core's answer. A
table declares its events. Routes, stored as rows, say who hears of each.
Each recipient's preference rows say how. Channel adapters
([runtime providers](/superschematic/guides/runtime-providers/)) send, and a reply runs as the
person who sent it.

| Built | Not built yet |
| --- | --- |
| `@event` on a table; the notifier's tables and the channel identities, which the build fills | SQLite and D1 |
| the same notifier in the Rust runtime, on Postgres ([in Rust](#in-rust)) | a route to an agent in Rust (D158 is TypeScript's alone so far) |
| the same notifier in the Go runtime, on Postgres ([in Go](#in-go)) | a route to an agent in Go (D158 is TypeScript's alone so far) |
| raising an event in the caller's transaction; Recurrence's opened, reminded and missed; a cleared reference | events raised by Comments (mentions, follows) into the same inbox |
| routes as rows, by kind, table and filter, to relation paths, principals, roles and parties | a generated server that builds the notifier into its `Deps` |
| the recipient's audience held at routing and at send time | a schedule that checks every adapter's health on its own |
| urgent, normal, digest and record; quiet hours; digests and briefs | an inbox page in Topcoat |
| deliveries that fall through, time out and escalate | an event at an occurrence's due time itself; a reply interpreter other than keywords |
| replies by reference, trust per channel kind, echo and undo, and the audit | |
| verifying an address by a code, and password reset by email | |

[D153](https://github.com/parable-work/superschematic/issues/602)
records the design and what is left. `examples/notifications` is the
worked example this page quotes.

## Declare the events

`@event` on a table names a kind, the fields its payload carries, the
field a notice is titled by and its default urgency. A table repeats it
per kind.

```ts
@event<Ticket>({ kind: "ticket.assigned", payload: ["title", "status"], title: "title" })
@event<Ticket>({ kind: "ticket.overdue", title: "title", urgency: "urgent" })
@event<Ticket>({ kind: "ticket.summary", title: "title", urgency: "digest" })
export abstract class Ticket { ... }
```

A behavior's events are kinds too: the use's member, a dot and the
behavior's kind. Recurrence emits `opened`, `reminded` and `missed` as it
moves an occurrence on, so `recurrence.missed` is a kind an `@event` may
make urgent:

```ts
@event<Duty>({ kind: "recurrence.missed", title: "title", urgency: "urgent" })
export abstract class Duty implements Recurrence<Duty, { ... }> { ... }
```

The four urgencies:

| Urgency | What happens |
| --- | --- |
| `urgent` | sent now, through quiet hours, and acknowledged; unacknowledged, it follows its route's escalation |
| `normal` | sent now, or when the recipient's quiet hours end |
| `digest` | batched into the recipient's next digest |
| `record` | a notice in the inbox, and nothing sent |

## Declare the notifier's tables

The notifier keeps its state in tables the database declares and the
build fills, one empty class per part, and the channel identities:

```ts
@notifier("inbox") export abstract class Notice {}
@notifier("deliveries") export abstract class Delivery {}
@notifier("routes") export abstract class NoticeRoute {}
@notifier("preferences") export abstract class NoticePreference {}
@notifier("subscriptions") export abstract class Subscription {}
@notifier("channelTrust") export abstract class ChannelTrust {}
@notifier("replies") export abstract class Reply {}
@queue() @notifier("dispatch") export abstract class NoticeWork {}
@channelIdentities() export abstract class ChannelIdentity {}
```

The inbox, the deliveries, the routes and the dispatch queue are
required. The notifier runs on Postgres. A database that declares any of
this needs a channel adapter: the build refuses an environment that runs
an API over it with no channel slot set.

## Raise an event

Raise an event in the transaction of the change that caused it:

```ts
await orm.transaction(async tx => {
  await tx.ticket.updateOne(id, { status: "overdue" });
  await notifier.raise(tx, { table: "Ticket", key: id, kind: "ticket.overdue" });
});
```

The event is a message of the dispatch queue: it commits with the change
or not at all, and its enqueue wakes the worker at once on Workers. A
behavior worker's Workers method hands a behavior's event over with
`notifier.fromBehaviorEvent(tx, "Duty", "recurrence", event)`, and the
worker of a references table's `cleared` queue with
`notifier.cleared(tx, message)`.

Run the notifier on a worker of the dispatch queue:

```ts
@worker({ queue: NoticeWork, concurrency: 4 })
export abstract class Notify {}
```

whose handler is `message => notifier.handle(message)`.

## Route it

A route is a row: the event kind (or `*`), a table, a filter in the
repositories' filter syntax, its recipients, an urgency in place of the
event's, and an escalation.

```ts
await orm.asSystem().noticeRoute.createOne({
  name: "Overdue tickets page their people",
  eventKind: "ticket.overdue",
  filter: { status: { ne: "closed" } },
  recipients: [{ path: "owner" }, { path: "watchers" }, { role: "oncall" }],
  escalation: [{ afterSeconds: 600, channels: ["email"] }, { afterSeconds: 1800, recipients: [{ role: "leads" }] }],
});
```

| Recipient | Who |
| --- | --- |
| `{ path: "owner" }` | the principals a relation path of the record holds: a field of ids, through to-one relations (`project.owner`) |
| `{ principal: id }` | one user |
| `{ role: "oncall" }` | the users holding the role |
| `{ party: { table, key } }` | a party row, through its grants |
| `{ agent: name }` | an agent (D158): the route puts a run request about the record on the dispatcher's queue, in place of a notice ([agent runs](/superschematic/guides/agent-runs/)) |

Expose the route table through table operations behind a permission, so
editing routes is one.

Each recipient reads the record through its table's repository as
themselves: its audience and their grants hold. One who cannot read it
gets nothing, and nothing records that they were skipped. The notifier
asks again when it sends, so one who lost the record since hears nothing.

## Send it

Each recipient gets a notice in the inbox, numbered among their own
(`#142`), and a delivery on the first channel their preferences name. A
preference row maps an event kind and an urgency (each or `*`) to channel
kinds in order, quiet hours, a time zone and a digest rule. A row with no
principal is the default, seed data the app writes.

```ts
await orm.asSystem().noticePreference.createOne({
  principal: null, eventKind: "*", urgency: "*", channels: ["sms", "email"],
  quietStart: "22:00", quietEnd: "07:00", timeZone: "Europe/Paris", digest: "FREQ=DAILY;BYHOUR=8;BYMINUTE=0",
});
```

A delivery is queued, then sent, delivered and read, or it ends failed or
acknowledged. A send that fails, or no delivery report in five minutes
from an adapter that reports them, falls through to the next channel. An
adapter whose health check fails is skipped, and the notifier raises
`channel.down`, which the routes send on the other channels. A delivery
that ended is kept 90 days, then `prune` deletes it; notices and replies
stay.

The dispatch queue's worker also runs digests and briefs. A subscription
row is a saved query, a recurrence rule (dated by the core's expansion),
a recipient and channels; the query runs as its recipient.

## Take replies

A relay's `@webhook` route hands its request to the notifier, or an
inbound pipeline calls `reply` with the sender normalised. An app whose
senders include parties hands it to the inbound path instead, which gives
a user's reply to the notifier
([outside parties and inbound messages](/superschematic/guides/parties/)):

```ts
app.post("/webhooks/sms/inbound", async c =>
  c.json(await notifier.inbound("SMS", { headers: Object.fromEntries(c.req.raw.headers), body: await c.req.text() })));
```

A sender whose address is a verified channel identity runs as its
principal. A reply names its notice by number (`ACK 142`, `done 142`)
unless exactly one waits on its sender on that channel. `ack` acknowledges
and stops escalation. Any other verb is a reply action the app gives for
the event's kind:

```ts
const markDone: ReplyAction = {
  permission: "tickets.work",
  describe: "Marked done",
  async run(ctx) { ... return before; },
  async undo(ctx, before) { ... },
};
createNotifier({ orm, dispatch: noticeWorkQueue, providers, replies: { "ticket.overdue": { done: markDone } } });
```

It runs in a transaction as the sender, held to its permission, through
the same repository write the operation's route makes. The channel kind's
trust row caps what a reply may use: `{ channelKind: "sms", level: "low",
permissions: [] }` lets a text acknowledge and nothing else, since a
sender id can be spoofed. A kind with no row is low. An address entered by
hand acknowledges alone. Every reply is a row of the replies table: its
channel, its sender, what read the text (`keyword`), what it ran, and its
undo. Every action is echoed back with `UNDO 142`.

## Verify an address, reset a password

`notifier.addIdentity` adds an unverified address; `startVerification`
sends a six-digit code to it, and a reply with the code, or
`confirmIdentity`, verifies it. `passwordReset({ notifier, orm, identity })`
sends a reset code to the address the login names, through the email
adapter, and `complete` sets the new password with the identity service's
`resetPassword`, which ends every session, and verifies the address.

## In Rust

The Rust HTTP runtime's `orm::notify` is the same notifier, on Postgres
(D191). The Postgres definition of a generated Rust types crate carries
each table's `@event` declarations and what each table is to the
notifier, so `Notifier::new` finds the inbox, the deliveries, the routes
and the rest by itself, and the crate re-exports the module as
`orm::postgres::notify`. It takes the database's `Tables`, whose keys and
behaviors a reply's action writes through, and the dispatch queue's
descriptor:

```rust
use superschematic_http_runtime::orm::notify::{approval_replies, Notifier, RaiseInput, ReplyInput};
use superschematic_http_runtime::queue::QueueDefinition;

let [(approve, approve_action), (deny, deny_action)] = approval_replies("Approval", "agents.approve");
let notifier = Notifier::new(orm::postgres::tables(pool.clone()), QueueDefinition::parse(queues::NOTICE_WORK_QUEUE)?)?
    .with_providers(deps.providers.clone())
    .with_reply("ticket.overdue", "done", Arc::new(MarkDone))
    .with_reply("approval.requested", approve, approve_action)
    .with_reply("approval.requested", deny, deny_action);

// In the transaction of the change that caused it.
notifier.raise(&tx, RaiseInput::of("Ticket", key, "ticket.overdue")).await?;
// The dispatch queue's worker.
run_worker(notifier.worker_queue(), move |message| { let n = notifier.clone(); async move { Ok(n.handle(message).await?) } }, options, shutdown).await?;
// A relay's @webhook route.
let outcomes = notifier.inbound("SMS", &request).await?;
```

A reply action is a `ReplyAction`: its permission, what its echo says, its
`run` and, when `undoable`, its `undo`. It runs on repositories of its
transaction that act as the sender, keep to what the sender reads and run
a behavior's command with the permissions the channel's trust leaves the
sender. `approval_replies` are the `approve` and `deny` of an approval a
route sends to the people who hold its permission: each decides the row
as the replier, refuses an agent run and an approval already decided, and
writes the state, who decided it and on which channel.

`from_behavior_event` takes a behavior worker's event from its Workers
method, `cleared` a cleared reference's message, and `PasswordReset` sends
a reset code through the email adapter and sets the password with the
identity service's `reset_password`. Rust has no ambient caller, so a
recipient's read of the record opens the repositories for the recipient
(`with_access`). The wall time of quiet hours and digests is read from
Postgres, which holds the zone database. A route that names an agent puts
a run request on the agent dispatcher's queue in the routing transaction,
once `with_agents` gives the notifier that queue (D193); without it, the
route's agent is logged and reaches no one. A party's address whose grants
have all ended since it was verified is sent nothing. A Rust types crate
that lists `sqlite` is refused the notifier, as every language is.

## In Go

The Go HTTP runtime's `notify` package is the same notifier, on Postgres
(D190). The generated Go ORM of a database that uses the notifier writes
`notifications.go`: the notifier's tables by part with their columns, each
table's key, events and to-one relations, and `Database.Notifications()`,
the host the notifier runs through. `notify.New` finds the inbox, the
deliveries, the routes and the rest from it:

```go
replies := map[string]map[string]notify.ReplyAction{
	"ticket.overdue":     {"done": markDone},
	"approval.requested": notify.ApprovalReplies("Approval", "agents.approve"),
}
notifier, err := notify.New(db.Notifications(), notify.Options{Providers: deps.Providers, Replies: replies})

// In the transaction of the change that caused it.
err = db.Transaction(ctx, func(tx orm.TxInterface) error {
	_, err := notifier.Raise(ctx, tx, notify.RaiseInput{Table: "Ticket", Key: key, Kind: "ticket.overdue"})
	return err
})
// The dispatch queue's worker.
err = worker.Run(ctx, notifier.WorkerQueue(), options)
// A relay's @webhook route.
outcomes, err := notifier.Inbound(ctx, "SMS", request)
```

A reply action is a `notify.ReplyAction`: its permission, what its echo
says, its `Run` and, when it can be undone, its `Undo`. It runs in a
transaction of the database, and its `ReplyContext` holds that transaction
(`Tx`, the ORM's `TxInterface`) and a context that acts as the sender
(`Ctx`): the repositories keep what it reads and writes to what the sender
sees, and a behavior's command runs with the permissions the channel's
trust leaves the sender. `ApprovalReplies` are the `approve` and `deny` of
an approval a route sends to the people who hold its permission: each
decides the row as the replier, refuses an agent run and an approval
already decided, and writes the state, who decided it and on which
channel.

A recipient reads the record through the record's repository as
themselves, held to its audience and their grants. A route's filter and a
brief's saved query are in the repositories' filter syntax, which the ORM
reads into the table's filter. `FromBehaviorEvent` takes a behavior's
event from the project's Workers method, `Cleared` a cleared reference's
message, and `notify.PasswordReset` sends a reset code through the email
adapter and sets the password with the identity service's
`ResetPassword`. The wall time of quiet hours and digests is read from
Postgres, which holds the zone database. Whether an adapter reports
statuses or reads inbound messages is whether it implements
`providers.ChannelStatusReader` and `providers.ChannelInboundReader`. A
route that names an agent is logged and reaches no one: the Go runtime
does not serve agent runs yet.
