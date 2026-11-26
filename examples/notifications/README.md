# notifications

A tickets app whose records raise events, which reach the right people on
the right channel, and whose people answer by text or email (D153). The
[Events and notifications](https://parable-work.github.io/superschematic/guides/notifications/)
guide covers it in full.

| Path | What it is |
|---|---|
| `schemas/services/tickets-db` | DB, Postgres: `Ticket` raises `ticket.assigned`, `ticket.overdue` (urgent), `ticket.summary` (digest) and `reference.cleared` (record), and its owner sees it with those it names; `Duty` implements Recurrence, whose `recurrence.missed` is urgent; the notifier's tables (`Notice`, `Delivery`, `NoticeRoute`, `NoticePreference`, `Subscription`, `ChannelTrust`, `Reply`) and its queue `NoticeWork`; `ChannelIdentity`, the addresses people are reached at and reply from |
| `schemas/services/tickets-api` | API over `tickets-db`: `TicketsApiConfig` names two channel slots, `SMS` and `MAIL`; `Routes` lists and creates routes behind `notifications.routes`; `Notify` drains the notifier's queue, `HearCleared` the queue a deleted ticket's references enqueue on, and `DutyDeadlines` fires each duty's deadlines |
| `server/server.ts` | The wiring: the notifier over the providers the config names, the relay's callbacks, the inbox, password reset, and the workers' handlers |

The channels are the SMS relay and the mail relay of
`examples/runtime-providers`. Swapping either for another adapter is the
environment's config alone.

## What it shows

- **Raising is part of the change.** An operation calls
  `notifier.raise(tx, { table: "Ticket", key, kind: "ticket.overdue" })` in
  its own transaction. The event commits with the change or not at all,
  and it wakes the worker at once on Workers.
- **Routes are rows.** A route matches `ticket.overdue`, names
  `{ path: "owner" }` and `{ path: "watchers" }`, and escalates to email
  after ten minutes. Editing routes needs `notifications.routes`.
- **A record never leaks through a notice.** A watcher who cannot read
  the ticket gets nothing, and nothing records that they were skipped.
- **Each person's channels are theirs.** A preference row lists the
  channels in the order a failure falls through, quiet hours and a digest
  rule. Urgent ignores quiet hours; digest waits for the digest.
- **A reply is input.** "ACK 142" by text acknowledges notice 142 and stops
  its escalation. "done 142" by email marks the ticket done as its sender,
  and the echo says how to undo it. By text, which `ChannelTrust` keeps
  low, "done" is refused. Every reply is a `Reply` row naming its channel
  and what read it.
- **Password reset goes by email.** `POST /api/auth/reset` sends a code
  through the mail relay; the code sets the new password and ends every
  session.

## Where it runs

`TestNotificationsExampleLoads` (`internal/loader/notifications`) loads
these schemas. The same scenario, under the fixture name
`fixture-notifications-db`, runs end to end against Postgres in
`TestNotificationsEndToEnd` (`internal/generator/tsgen`), through the two
relays over stand-in services.

The notifier is the TypeScript runtime's, on Postgres: the Go and Rust
servers have none yet, and the generated server does not build one into
its `Deps`, which `server/server.ts` wires.
