---
title: Selling and booking
description: Sell counted stock and book time with Stock, Calendar, Holds and Payments on tables that stay tables, through the behaviors' core. The supply contract, totals and currency, payments through a provider adapter of your own (webhooks, a settle job, idempotency, no card data), refunds two staff sign off with Approvals, expiry with Timers and a behavior worker, the MCP tools' policy for operations that move money, and what a take locks.
sidebar:
  order: 8.5
---

A shop sells two kinds of supply: units it counts, such as tins of tea,
and time on a resource, such as a fifteen-minute pickup slot at a
counter. A claim, a checkout or a booking, holds some of each while it
waits for money, keeps what it holds once paid, and lets it go when it
expires or is cancelled. Four behaviors do this (D70, D103):

| Behavior | On | What it adds | Requires |
| --- | --- | --- | --- |
| [`Stock`](#stock) | what is counted: an inventory row, a ticket type | units on hand and held, what is available, and a ledger of every movement | |
| [`Calendar`](#calendar) | what is booked: a counter, a stylist, a room | opening hours in a time zone, a capacity, a slot grid, lead time and horizon, closures, and free slots across rows | |
| [`Holds`](#holds) | the claim: a checkout, a booking, a rental | its holds on any supply, taken, committed and released as its Workflow moves | `Workflow` |
| [`Payments`](#payments) | what is paid: a checkout, an invoice, a refund | what it owes, the attempts that pay it and the refunds that give it back, settled by an adapter of your own | |

A DB table implements each with `implements`, beside a `status:
Workflow<{ ... }>` field, and stays a table: each behavior's state is
columns of the table and sub-tables beside it, which the migrations plan
as any other, and its operations run through the behaviors' core in the
repositories' transactions, in Go, TypeScript and Rust alike
([Units and time a claim holds, and what it owes](/superschematic/guides/database-tables/#units-and-time-a-claim-holds-and-what-it-owes)).
They build on the core's other behaviors: `Timers` lets a hold expire,
`Approvals` gates a refund, a `Numbering` field numbers orders and
refunds, and `StateRules` freezes a placed order
([Fields a table keeps, freezes and signs off](/superschematic/guides/database-tables/#fields-a-table-keeps-freezes-and-signs-off)).
The acme shop sells and books through them ([the acme shop](#the-acme-shop)).
A booking that holds a resource's time and waits for its payment is the
`Reservation` bundle, which presets Workflow, Holds, Payments and a
Timers expiry, so the booking says only its resource, its interval, its
amount and its currency
([Bundles](/superschematic/guides/database-tables/#bundles-behaviors-with-their-wiring-preset)).

## Set up

The core declares the four behaviors, so every binary's meta-schema
admits a table that implements them, and `@superschematic/db` types
their configs: `Stock<T, C>`, `Calendar<T, C>`, `Holds<T, C>` and
`Payments<T, C>`, each resolving to the empty object type, as `Timers`
does. Their rules are ported onto the behaviors' core, which every
language embeds: the Go ORM links its archive, the TypeScript
repositories load its WebAssembly module from `@superschematic/rules`,
and the Rust repositories build `superschematic-rules-adapter` and
`superschematic-rules-behaviors`, unpublished crates a checkout names
with the naming file's `[paths] rules_adapter_rust` and
`rules_behaviors_rust`.

An API serves a table's behavior operations with a class marked
`@behaviorOperations(Table)`, and runs its deadlines and its change
queue with a class marked `@behaviorWorker(Table)`
([A table's behavior operations](/superschematic/guides/api-routes/#a-tables-behavior-operations),
[A table's deadlines](/superschematic/guides/api-routes/#a-tables-deadlines-timers-and-its-worker)).
An implementation of your own calls the same operations on the
database's repositories: `orm.checkout.status.transition(id, { to:
"held" })` in TypeScript, `DB.GetCheckoutRepository().Status().Transition(...)`
in Go, `repositories.checkout_status()?.transition(...)` in Rust.

The permissions the behaviors check (`stock.write`, `payments.record`,
`refunds.approve`) are the caller's, from its session's roles
([users and sessions](/superschematic/guides/users-and-sessions/#roles-and-capabilities)):
a generated route runs its operation as the request's user with the
permissions their roles hold, and an implementation's call runs as the
context's user, holding none, unless it names another caller
(`orm.WithBehaviorCaller` in Go, `orm.withBehaviorCaller` in TypeScript,
`Repositories::with_caller` in Rust). The payment adapter names one that
holds `payments.record`.

## A shop's types

Inventory it counts and prices, a counter that opens fifteen-minute
pickup slots, and checkouts that hold both while they wait for payment.
A checkout's pickup counter is a to-one relation, which its `pickup`
hold names by its field:

```ts
import { Generic, Identity, Temporal } from "superscalar";
import { Nullable, jsonField } from "@superschematic/schema";
import { AutoGenerate, Calendar, Holds, Numbering, Payments, Relation, StateRules, Stock, Timers, Workflow, key } from "@superschematic/db";

export abstract class Inventory
  implements
    Stock<Inventory, {
      price: { field: "priceCents"; currency: "GBP" };
      lowAt: { field: "reorderAt" };
      maxPerHold: 99;
      permission: "stock.write";
    }>
{
  @key
  id: Identity.UUID;
  sku: string;
  priceCents: Generic.Int64;
  reorderAt: Nullable<Generic.Int64>;
}

export abstract class CollectionPoint
  implements
    Calendar<CollectionPoint, {
      timeZone: "Europe/London";
      hours: { mon: [["09:00", "12:30"], ["13:30", "17:30"]]; tue: [["09:00", "17:30"]]; sat: [["10:00", "16:00"]] };
      capacity: 4;
      slotMs: 900000;
      minMs: 900000;
      maxMs: 900000;
      bufferMs: { after: 300000 };
      leadMs: 7200000;
      horizonMs: 5184000000;
      permission: "calendar.manage";
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  name: string;
}

@jsonField
export abstract class CheckoutLine {
  inventoryId: Identity.UUID;
  quantity: Generic.Int64;
}

export abstract class Checkout
  implements
    Holds<Checkout, {
      holds: {
        lines: { each: { field: "lines"; target: "inventoryId" }; schema: "Inventory"; quantity: { field: "quantity" } };
        pickup: { link: "counter"; interval: { start: "pickupAt"; durationMs: 900000 } };
      };
      takeOn: ["held"];
      commitOn: ["placed"];
      releaseOn: ["expired", "cancelled"];
    }>,
    Timers<Checkout, {
      deadlines: {
        holdExpiry: { start: { enters: "held" }; afterMs: 900000; then: { transition: "expired" }; until: { leaves: "held" } };
        backstop: { start: { enters: "held" }; afterMs: 7200000; then: { transition: "expired" }; until: { leaves: "held" } };
      };
    }>,
    Payments<Checkout, {
      amount: { holds: true };
      require: { placed: "paid" };
      voidOn: ["expired", "cancelled"];
      afterRelease: "refund";
      refunds: { permission: "payments.refund" };
      record: { permission: "payments.record" };
    }>,
    StateRules<Checkout, { states: { held: { frozen: ["lines", "counterId", "pickupAt"] }; placed: { frozen: "all" } } }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  lines: CheckoutLine[];
  counter: Nullable<Relation<CollectionPoint>>;
  pickupAt: Nullable<Temporal.DateTime>;
  status: Workflow<{
    states: ["cart", "held", "placed", "expired", "cancelled"];
    initial: "cart";
    transitions: [
      { from: "cart"; to: "held" },
      { from: "held"; to: "placed" },
      { from: "held"; to: "expired" },
      { from: "held"; to: "cancelled" },
      { from: "cart"; to: "cancelled" }
    ];
    outcomes: { expired: "failure"; cancelled: "neutral" };
  }>;
  number: Numbering<{ format: "ORD-{yyyy}-{seq:5}"; reset: "yearly" }>;
}
```

A money field a behavior reads is an integer, `Generic.Int64`, of the
currency's minor units; `@display({ currency: "GBP" })` shows it as
money. `Refund` is under [refunds with Approvals](#refunds-with-approvals).
The build checks what a type's own check cannot see: a hold names a
table of the database that implements `Supply` and asks it what it
holds, an amount from `Holds` lists `Holds` before `Payments`, and a
refund's `refundOf` is a required to-one relation to a table that
implements `Payments` with the same `record.permission`.

## The supply contract

`Stock` and `Calendar` implement the core's contract `Supply`: they
serve the row operations `holdFor`, `commitFor`, `releaseFor` and, for
counted supply, `returnFor`, and the read-only `checkHold`. `Holds` finds
the supply's use by its declaration and calls them on the supply's row
through the core, in the same transaction, so it holds a supply of your
own, whose declaration lists `{ "name": "Supply", "role": "implements" }`
among its contracts, as it holds the core's, and it never reads a
supply's sub-tables. Each takes the same parameters:

```json
{ "holder": { "schema": "Checkout", "id": "c-1" }, "hold": "lines", "item": 0, "quantity": 2 }
{ "holder": { "schema": "Checkout", "id": "c-1" }, "hold": "pickup", "item": 0, "interval": { "startMs": 1793000000000, "endMs": 1793000900000 } }
```

- **The supply checks the claim.** A supply takes a writing call only
  for an item the holder's own `Holds` items already show, asking what
  the call asks, in the state the call moves the supply's record to.
  `Holds` writes the item's new state before it calls, in the same
  transaction, so a caller who reaches `holdFor` or `releaseFor` through
  a route with made-up parameters is refused `not_requested`. A supply's
  record of each item, by holder, hold and item, moves only forward, so a
  call made again changes nothing.
- **What does not fit is `unavailable`.** Its details name the supply,
  the hold and item, what was asked and what is free: `available` for a
  count, the `conflict`ing interval for time. A supply adds codes of its
  own with the same details (`outside_hours`, `too_soon`).
- **`checkHold`** answers whether `holdFor` would fit now, without the
  holder check or a write, counting what the claim's other items still
  to hold ask of the same row (`also`), so two lines of one product are
  checked together.
- **An optional price.** A supply may name an own integer field as its
  unit price, with a currency. `holdFor` returns it and `Holds` keeps it
  on the item, so a line keeps the price it was held at, and the shopper
  never sets it.
- **A supply's delete** is refused while it holds anything (`held`): an
  item held, or committed time that has not ended.

`Stock` and `Calendar` declare the same operations and conflict; five
bikes rented by the hour are a `Calendar` with capacity 5.

## Stock

| | |
| --- | --- |
| Config | `permission` (required) for the operations that change the count; `price`: `{ field, currency }`, an own integer field and a code or an own `Finance.Currency` field; `lowAt`: a count or `{ field }`; `maxPerHold`; `backorder` (`false` when absent) |
| Columns | `stock_on_hand`, `stock_held`, which only Stock's operations change; `available`, `onHand − held`, never below 0 unless `backorder`, and `low`, true at or below `lowAt`, are computed at a read, so a list does not filter on them yet: read the two columns and `lowAt`'s field |
| Sub-tables | `holds`, the record of each claim item it holds or held; `movements`, its ledger |
| Operations | `receiveStock({ quantity, reason, reference? })`, `adjustStock({ delta, reason })`, `countStock({ onHand, reason })`, each needing `permission`; the read-only `listStockMovements({ limit?, cursor? })`, newest first; the contract's operations |
| Vetoes | `unavailable` (`details.available`), `over_max`, `not_requested`, `below_held` (a count below what is held, unless the caller holds the permission's `.override`; an adjustment down past it unless `backorder`), `held` (a delete while holding) |

Every change of what is on hand is a row of the append-only ledger: its
kind (`receive`, `adjust`, `count`, `commit`, `return`), delta, what was
on hand after it, the reason and reference, the claim and item it was
for, who made it and when. `stock_on_hand` always equals the sum of the
movements, and `stock_held` the sum of what the supply's records hold. A
commit takes the units out of both: they are sold. A raw SQL write of
either column runs none of these rules, and the counts and the ledger
stop agreeing.

## Calendar

| | |
| --- | --- |
| Config | `timeZone` (IANA); `hours`: by day `mon` to `sun`, `[["HH:MM", "HH:MM"], …]` in the zone; `capacity`: a count or `{ field }`; `slotMs`, the start grid from each range's opening; `minMs`, `maxMs`; `bufferMs: { before?, after? }`; `leadMs`, `horizonMs`, against the core's clock; `permission`, for a row's own hours and closures |
| Columns | none |
| Sub-tables | `bookings`, the record of each item; `closures`; `settings`, a row's own hours and time zone |
| Computed at a read | `timeZone`, `hours` (the row's own, or the config's), `closures` (those that have not ended, at most 100) |
| Operations | `setOpeningHours({ hours?, timeZone? })`, `addClosure({ startMs, endMs, reason, force? })`, `removeClosure({ closure })`; the read-only `listBookings({ startMs, endMs, limit?, cursor? })` and, on the table as a whole, `findAvailability({ startMs, endMs, durationMs, ids?, where?, limit? })`; the contract's operations but `returnFor` |
| Vetoes | in `holdFor`'s order: `outside_hours`, `closed`, `off_grid`, `too_short`, `too_long`, `too_soon`, `too_far`, `unavailable` (the conflicting hold at the peak); `has_bookings` (a closure over held time unless forced with the permission's `.override`, a zone change while time is held); `window_too_long` (past 31 days); `no_closure`; `held` |

Capacity counts the most holds over any instant of the interval, each
with its buffers, so a long hold two short ones overlap at different
times is counted once. Local times become instants through `jiff` and
its bundled time zone database, so every language's repositories compute
the same slots: a wall time a clock change skips has no slot, and a
repeated one gives two. `findAvailability` lists free slots across up to
100 rows, at most 500, soonest first:

```ts
const free = await orm.collectionPoint.calendar.findAvailability({ startMs, endMs: startMs + 7 * DAY_MS, durationMs: 900000, limit: 10 });
// { slots: [{ id: "<counter>", startMs: 1793000000000, endMs: 1793000900000, remaining: 4 }, …], truncated: false }
```

Two resources booked together, a stylist and a chair, are two calls the
caller intersects.

## Holds

| | |
| --- | --- |
| Config | `holds`: by name, a `link`, a to-one relation of the table, or `each: { field, target }` with `schema`, an own list whose elements name a supply's key; each with a `quantity` (a count or `{ field }`) or an `interval` (`{ start, end }` or `{ start, durationMs }`, date-time fields); `takeOn`, `commitOn`, `releaseOn`, `returnOn`: states of the table's Workflow, or of the one `status` names (`takeOn` absent: at the create); `maxItems` (100) |
| Columns | `holds_state`, the least advanced item's (`none`, `pending`, `held`, `committed`, `released`, `returned`); `holds_total`, `{ amount, currency }` ([totals](#totals-and-currency)) |
| Sub-tables | `items`, each item the claim has taken: `{ hold, item, schema, id, quantity or startMs and endMs, state, unitPrice?, currency? }`, which `listHolds` reads |
| Operations | `retakeHolds`, which takes again in a `takeOn` state after a supply refused; the read-only `listHolds`, with each supply's `checkHold` answer for an item still pending |
| Vetoes | `unavailable` (every item that does not fit, in `details.items`), `holds_missing` (a move into a `commitOn` state while an item is not held), `held_fields` (an update of what a hold reads while the claim holds), `committed` (a delete while committed time has not ended), `too_many_items`, `not_taking`, `no_supply` |

- **All or none.** Entering a `takeOn` state, its guard asks each
  supply's `checkHold` and refuses with every item that does not fit;
  then each item is held through the supply's `holdFor` in the same
  transaction, so a claim holds every item or none. A cart in a state
  before `takeOn` shows its items `pending`: what it would hold.
- **Moving.** Entering a `commitOn` state commits each held item, a
  `releaseOn` state releases them, and a `returnOn` state returns
  committed units. `Holds` reads the status the write left, so a
  `Timers` fire, a `Reactions` move and a caller's `transition` are
  caught alike. It has no release operation: a claim lets go by a move
  of its status, which runs its guards.
- **Deleting** a claim releases what it holds and leaves what it
  committed: units stay sold and time booked.

## Totals and currency

An amount is a whole number of a currency's minor units, 450 for £4.50,
and whatever holds one names its currency: an ISO 4217 code, as a
literal (`"GBP"`) the core checks or an own field of superscalar's
`Finance.Currency`. Amounts of two currencies are never added.

`Holds`' total sums the priced items' unit prices times their
quantities. An item without a price, such as a pickup slot, adds
nothing; with none priced the total is `{ amount: 0, currency: null }`.
It is null when the priced items' currencies differ or the sum passes
what a JSON number holds exactly.

`Payments` with `amount: { holds: true }` fails closed: while an item is
`pending` its price is not known, so what the claim owes is unknown, and
`openPayment` and a move into a `require` state are refused
`total_unknown`. A claim whose held items carry no price owes 0, so a
free booking moves where `require` waits for money. Discounts, tax and
promotions are the API's: it computes a total into an own field and
`Payments` reads `amount: { field }`.

## Payments

| | |
| --- | --- |
| Config | `amount`: `{ field }` or `{ holds: true }`; `currency` for an amount from a field; `capture`: `automatic` or `manual` with `captureOn`; `require`: `{ <state>: "authorized" or "paid" }`; `voidOn`; `afterRelease`: `refund` or `keep`; `refunds: { permission }`; `record: { permission }` (required: the adapter's); `refundOf: { link }` and `issueOn` on a refund table |
| Columns | `payments_due`, `payments_authorized`, `payments_captured`, `payments_refunded`, `payments_outstanding`, `payments_overpaid`, `payments_currency`, `payments_state` (`unpaid`, `pending`, `authorized`, `paid`, `partiallyRefunded`, `refunded`), each of which a list filters on |
| Sub-tables | `attempts`, `actions`, `refunds`, `issues`; and a shared `events` table, one row per provider event |
| Operations | `openPayment({ provider, idempotencyKey, returnUrl? })`, `capturePayment`, `voidPayment`, `refundPayment({ amount, reason, idempotencyKey })`; the adapter's `recordPaymentEvent` and, on the table as a whole, `listPaymentActions({ provider })`; `refundFor` and `settleRefundFor`, which a refund row and the row it refunds call on each other |
| Vetoes | `nothing_due`, `total_unknown`, `amount_locked`, `currency_mismatch`, `over_capture`, `exceeds_refundable`, `unpaid`, `unauthorized`, `payment_in_progress`, `no_attempt`, `not_linked`, `key_reused`, `attempt_closed`, `not_configured` |

**No behavior calls a provider.** `openPayment` records an attempt and
an action for the provider, and returns the attempt, its status and its
amount; the same key returns the same attempt. An adapter, code in your
API, does what each action asks and reports back:

```
shopper ──▶ API ──repositories──▶ core: openPayment → { attempt, amount, currency, status }
              │  provider: create a payment, Idempotency-Key = the action's id
              ▼
          provider ──hosted fields──▶ browser    (card data never reaches the shop)
          provider ──webhook──▶ API @webhook @hmacVerified ──▶ core: recordPaymentEvent
          settle job ──▶ core: listPaymentActions ──▶ provider: capture, void, refund
                                                  └─▶ core: recordPaymentEvent
```

- **The webhook.** An operation marked `@webhook @hmacVerified({ provider })`
  ([webhooks](/superschematic/guides/api-routes/#webhooks)) checks the
  provider's signature before anything else, maps each event to
  `recordPaymentEvent({ provider, eventId, attempt or providerRef, kind,
  amount, currency, at })`, and answers quickly: 2xx for an event it does
  not know and for a veto such as `currency_mismatch`, which it logs, so
  the provider does not retry forever, and 503 for `busy`, so it does.
- **The settle job.** A [job](/superschematic/guides/stacks/#jobs),
  every few minutes, reads `listPaymentActions` for its provider, calls
  the provider for each with the action's id as the provider's
  idempotency key, and records the outcome. It also asks the provider
  about attempts stuck in `processing`, which it reads from the
  `attempts` sub-table, as the core does: no repository reads a
  sub-table. Two runs of the job may read the same action; the
  provider's idempotency key gives the second the first's result, and
  recording it again changes nothing.
- **Idempotency.** An event applies once per `(provider, eventId)` on a
  table: a delivery repeated changes and writes nothing, and the same id
  with other content is `key_reused`. An attempt's status only moves
  forward (`open < requiresAction < processing < failed < authorized <
  cancelled < captured`), so a `processing` that arrives after
  `captured` is recorded and ignored, and providers' delivery order does
  not matter.
- **The adapter's caller.** The webhook and the job run their commands
  as a caller of their own that holds `record.permission`, through the
  repositories' behavior caller. End users never hold `payments.record`.
- **Secrets.** The provider's API key and signing secret are `Secret<T>`
  fields of the API's config ([stacks](/superschematic/guides/stacks/#wire-the-services)),
  kept in each platform's secret store, never in a table or a log.
- **No card data.** Card details go only to the provider's hosted page
  or fields, which keeps a merchant to PCI DSS SAQ A. Every parameter
  schema of Payments is closed: `providerRef` is limited by pattern, the
  display hint is a `brand` and a four-digit `last4`, and metadata is at
  most ten string pairs. A provider's raw event, which holds the payer's
  name, email and address, is never stored. Add no card fields to a
  schema of your own.

**The Workflow.** `require: { placed: "paid" }` refuses a move into
`placed` until what is due is captured (`unpaid`), or authorized for
`"authorized"`. Entering a `voidOn` state voids open attempts. No move
leaves a state while an attempt is `processing` (`payment_in_progress`),
so an expiry does not let go of what is being paid for
([expiry](#expiry-with-timers)). Money captured after the claim let go,
a webhook after the expiry, is refunded with `afterRelease: "refund"`.

**Placing once paid.** A table's `Reactions` rule names a field of the
table, and a member path, `Payments.state`, waits for a later step
(D103), so no rule hears the state turn `paid`. The adapter makes the
move: once a capture it records leaves a held checkout's `payments_state`
`paid`, it moves the checkout to `placed`, which `require` now allows,
and the settle job places any a webhook left paid and held, a list
filtered on `status` and `paymentsState`.

**Paying twice.** A provider may still finish a payment it reported
failed. `openPayment` gives each earlier failed attempt a `void` action,
and a capture that pays what is due voids the other open ones. A capture
that arrives anyway is counted: what it brings past what is due is
`payments_overpaid`, which Payments asks back with a refund, leaving the
state `paid`.

### Refunds with Approvals

A shop without sign-off refunds with `refundPayment`, which needs
`refunds.permission` and is refused `exceeds_refundable` past what one
captured attempt has left. A refund that needs two staff is a row of its
own, whose relation names the checkout it refunds:

```ts
export abstract class Refund
  implements
    Approvals<Refund, { gates: { approved: { count: 2; permission: "refunds.approve" } } }>,
    StateRules<Refund, { states: { requested: { required: ["amountCents", "reason"] }; approved: { frozen: "all" } } }>,
    Payments<Refund, {
      amount: { field: "amountCents" };
      currency: "GBP";
      refundOf: { link: "checkout" };
      issueOn: ["approved"];
      record: { permission: "payments.record" };
    }>
{
  @key
  id: AutoGenerate<Identity.UUID>;
  checkout: Relation<Checkout>;
  amountCents: Generic.Int64;
  reason: string;
  // Whom the repositories stamp: Approvals counts no sign-off of theirs.
  createdBy: Identity.UUID;
  status: Workflow<{
    states: ["requested", "approved", "issued", "rejected", "failed"];
    initial: "requested";
    transitions: [
      { from: "requested"; to: "approved" },
      { from: "requested"; to: "rejected" },
      { from: "approved"; to: "issued" },
      { from: "approved"; to: "failed" }
    ];
  }>;
  number: Numbering<{ format: "RF-{yyyy}-{seq:5}"; reset: "yearly" }>;
}
```

1. Two holders of `refunds.approve` sign it off; its creator, whom
   `createdBy` holds, may not (`creator_cannot_sign`), and a move into
   `approved` with one sign-off is `approvals_missing`
   ([A table's sign-offs](/superschematic/guides/api-routes/#a-tables-sign-offs-approvals)).
   The gate excludes the creator unless it says `excludeCreator: false`,
   so a table without `createdBy` is refused at the build.
2. Entering `approved` invokes `refundFor` on the checkout the relation
   points at, which checks the relation and the amount and records the
   refund and its action. One approval pays one refund, whatever key a
   caller who reaches `refundFor` makes up.
3. The settle job sends the refund to the provider, and records its
   outcome on the checkout (`kind: "refunded"`, with the refund's id),
   which invokes `settleRefundFor` back on the refund, as the adapter: its
   `record.permission` must be the checkout's.
4. The refund's `payments_state` turns `refunded`, and the settle job,
   which lists the approved refunds in that state, moves each to
   `issued`.

## Expiry with Timers

`Holds` has no expiry of its own. A `Timers` deadline moves the claim
into a `releaseOn` state, as `holdExpiry` does above, and the release
gives the units and the slot back. A held checkout's deadlines are
messages of its table's deadline queue, which an API's
`@behaviorWorker(Checkout)` claims as each falls due and runs through the
core ([the worker](/superschematic/guides/api-routes/#the-worker)).

- While no worker runs nothing expires, and when one starts every
  overdue deadline fires. A stack's environment that runs the database
  and no worker for a table's queues is refused.
- **The late payment.** A shopper pays at 14:59:58 and the hold expires
  at 15:00. The attempt is `processing`, so `Payments` vetoes the move,
  and the worker releases the deadline's message, due again a minute
  later, while the checkout stays held. When the capture is recorded,
  the adapter places it. A second, longer deadline, `backstop`, lets go
  of a claim whose payment never reports.
- A worker's commands hold the permissions its decorator gives it: a
  `then` whose transition names a permission the worker lacks fires and
  records `forbidden`.

## MCP tools and agents

Every operation a `@behaviorOperations` set serves is an MCP tool of
the API, `checkout_payments_open_payment` say, with the behavior's
declaration's invocation policy
([MCP tools](/superschematic/reference/mcp-tools/#a-tables-behavior-operations)),
and every operation of yours is one with its `@mcp`. So an agent can
sell and book, and what it may do unasked is the API's to say:

- **An operation of yours.** `@mcp({ handle, invocationPolicy: "ask" })`
  makes an agent's client confirm each call. The choice is the
  deployment's: the acme shop asks before it pays for a checkout, asks
  for a refund or counts a shelf, but lets an agent book a delivery in
  unasked, since a receipt is routine and another movement reverses it.
- **A behavior's operation.** Its tool takes the policy the behavior's
  declaration names, else the default, and an API's own policy, its
  hiding of the tool and a handle of its own are not available for it
  yet. A set whose `@requireService` admits only calling services
  publishes no tool, since no end user's agent can call it. Serve the
  operations that move money from routes of your own, as the acme shop's
  `payCheckout` does, when an agent must ask first.

## Concurrency and throughput

A take locks each supply row it reads, `Supply/<table>/<key>`, in key
order: a transaction's advisory lock on Postgres, and on SQLite and D1 a
check in the unit that writes that each row read under the lock is
still at its version. Each write of a supply's row is held to its
version too, so a claim that lost the race runs again over the winner's
hold and is `unavailable`. Writes of other supplies do not wait.
`TestCommerceTablesEndToEnd` and `TestCalendarTablesEndToEnd` move two
claims for the last unit, and two bookings of one slot, at once through
the generated repository on Postgres: one holds. Postgres has no
exclusion constraint for `Calendar`: one would hold a capacity of 1
alone, with no buffers.

## Personal data

`Payments` keeps holder references, amounts, the provider's reference
and a card's brand and last four digits; `Holds` and `Calendar` keep
references and intervals. A claim's own columns may hold more, a
delivery address or a phone number, and its change queue holds each
write's images until its worker runs them. Keep the payer's identity
with the provider.

## The acme shop

`examples/acme-shop` is the worked example. Its shop database's tables
are `Inventory` (`Stock`), `CollectionPoint` (`Calendar`), `Checkout`
(`Holds`, `Timers`, `Payments` and `StateRules`, with `status` and
`number` fields and its `counter` a relation) and `Refund` (two
`Approvals` sign-offs, `StateRules`, `Payments` with `refundOf`, and its
`status` and `number`). Each service plays one part:

- `shop-cart`, a TypeScript API, is the cart: a shopper's cart is a
  `Checkout` row in `cart`, written through its repositories, the pickup
  slots are `CollectionPoint`'s `findAvailability`, and staff who hold
  `calendar.manage` open a counter through a route the build serves from
  the table (`@tableOperation`);
- `shop-orders`, in Go, moves a checkout from `cart` to `held`
  (`startCheckout`), which holds every line's units and the slot or none,
  recording the address in the same transaction; opens its payment and
  makes the intent at the provider (`payCheckout`); receives the
  provider's events at `paymentEvents`, a webhook verified by
  `@hmacVerified` (a `t=…,v1=…` HMAC with a five-minute tolerance),
  recording each under the event's id, and places a checkout its capture
  left paid, which commits its units and slot; and asks for refunds
  (`requestRefund`). Its job `SettlePayments` makes the payment actions
  the checkouts hold at the provider, each with the action's id as its
  idempotency key, asks after attempts left processing, places what a
  webhook left paid, issues refunds the provider made, and writes the
  `Order` row, unique by `checkoutId`, of each placed checkout, as the
  webhook and `getCheckout` do when they find it placed. `shop-orders`
  then tells `shop-stock` what the checkout sold;
- `shop-stock`, in Rust, keeps the stock over `Inventory`: staff count a
  product's shelf and book deliveries in, each a Stock operation through
  the Rust repositories, and `stockSold`, which only `shop-orders` may
  call (`@requireService`), enqueues `StockLow` for a product a sale left
  low. Its job `OrderRestocks` files a restock request for each
  `Inventory` it reads low;
- `shop-api`, in Go, serves `Refund`'s operations to staff who hold
  `refunds.approve` (`RefundActions`), and runs `Checkout`'s deadlines
  with its behavior worker `CheckoutTimers`, which expires a checkout
  held too long;
- `acmepay`, a fake provider in Go, takes test tokens only and refuses
  card numbers, signs its webhooks and honours idempotency keys. `stack
  dev` and the tests pay through it without a real provider: it runs
  beside `stack dev` on `127.0.0.1:4250`, and in-process in the tests.

`go/commerce_test.go` runs a sale end to end on a SQLite file of the
shop's tables, and on Postgres when `ACME_SHOP_DATABASE_URL` names a
server: a checkout holds two units and a slot, another finds one free,
acmepay's signed capture places it, its order is written once, a refund
two staff sign off is made and issued, and a held checkout's
`holdExpiry`, run as the worker runs it, expires it, freeing its units
and its slot and voiding its payment.

`placeOrder` still takes an order in one step, holding no stock and
taking no payment, for the shop's Rust server, which answers the
checkout routes and the webhook 501, and its Topcoat app. The
[README](https://github.com/parable-work/superschematic/blob/main/examples/acme-shop/README.md#commerce)
walks through a sale.

## The back office

The Topcoat extension renders no panel of a table's behaviors yet, and
refuses an API whose operations import a table that implements one, so
the acme shop serves its staff's commerce from APIs it builds no Topcoat
crate for, and from Topcoat pages of its own operations: staff book a
delivery in on a product's stock page in `shop-stock`'s Backoffice
(`/stock/stock/<product id>`), whose form `receiveStock`'s `@docs`
titles, and sign refunds off through `shop-api`'s routes. Pages of a
table's behaviors, the stock's ledger, a counter's week and bookings, a
checkout's holds and payment as money and a refund's sign-offs, wait for
behavior panels on tables.

## Where to go next

- [Database tables](/superschematic/guides/database-tables/#units-and-time-a-claim-holds-and-what-it-owes):
  what each behavior keeps on a table, and the bundles that preset them.
- [API routes](/superschematic/guides/api-routes/#a-tables-behavior-operations):
  a table's behavior operations, its deadlines and its worker, its
  reactions, and its sign-offs.
- The acme shop's
  [README](https://github.com/parable-work/superschematic/blob/main/examples/acme-shop/README.md):
  a cart, an orders API with a payment adapter and its webhook, a stock
  API and a fake provider, in three languages.
