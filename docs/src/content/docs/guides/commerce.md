---
title: Selling and booking with the engine
description: Sell counted stock and book time on the engine with Stock, Calendar, Holds and Payments. The supply contract, totals and currency, payments through a provider adapter outside the engine (webhooks, a settle job, idempotency, no card data), refunds two staff sign off with Approvals, expiry with Timers, the MCP tools' policy for operations that move money, and what one namespace's writer allows.
sidebar:
  order: 8.5
---

A shop sells two kinds of supply: units it counts, such as tins of tea,
and time on a resource, such as a fifteen-minute pickup slot at a
counter. A claim, a checkout or a booking, holds some of each while it
waits for money, keeps what it holds once paid, and lets it go when it
expires or is cancelled. Four behaviors do this (D70):

| Behavior | On | What it adds | Requires |
| --- | --- | --- | --- |
| [`Stock`](#stock) | what is counted: an inventory record, a ticket type | units on hand, held and available, and a ledger of every movement | |
| [`Calendar`](#calendar) | what is booked: a counter, a stylist, a room | opening hours in a time zone, a capacity, a slot grid, lead time and horizon, closures, and free slots across instances | |
| [`Holds`](#holds) | the claim: a checkout, a booking, a rental | its holds on any supply, taken, committed and released as its Workflow moves | `Workflow` |
| [`Payments`](#payments) | what is paid: a checkout, an invoice, a refund | what it owes, the attempts that pay it and the refunds that give it back, settled by an adapter outside the engine | |

They build on [the core's behaviors](/superschematic/guides/engine-behaviors/):
`Timers` lets a hold expire, `Approvals` gates a refund, `Numbering`
numbers orders and refunds, `StateRules` freezes a placed order, and a
`Reactions` rule places a checkout once it is paid. The acme shop sells
and books through these behaviors ([acme-shop](#the-acme-shop)).

This page assumes you know [the engine](/superschematic/guides/engine/)
and [engine behaviors](/superschematic/guides/engine-behaviors/). The
package's
[README](https://github.com/parable-work/superschematic/blob/main/runtime/engine-commerce/README.md)
is the full reference for every refusal, event and config change.

## Set up

The core declares the four behaviors with the package
`@superschematic/engine-commerce`, so every binary's meta-schema admits a
schema that composes them, and `@behavior` types their configs. They are
Rust, the crate `superschematic-engine-commerce`
(`runtime/engine-commerce/rust`), which runs only where a deployment
names it:

- A generated Go or Rust server links it through the project's engine
  crate, which the build writes when a DB schema's types compose a
  commerce behavior, at the `[paths]` key `engine_commerce_rust`
  ([the project's engine crate](/superschematic/guides/engine-behaviors/#the-projects-engine-crate)).
- A TypeScript server opens the engine on the core's addon, which carries
  them, and names them in the engine's `behaviors` option. The generated
  server does this itself; a server you write passes `commerceBehaviors`
  from `@superschematic/engine-commerce`, which also types their configs
  and records:

```ts
import { openEngine } from '@superschematic/engine';
import { commerceBehaviors } from '@superschematic/engine-commerce';

const engine = openEngine({ path: 'shop.db', policy, metaSchema, behaviors: commerceBehaviors, runner: { principal: runner } });
engine.runner.start();   // expiry, Reactions and refunds' follow-ups run on the runner
```

The permissions the behaviors check (`stock.adjust`, `payments.record`,
`refunds.approve`) are the caller's, from its session's roles
([users and sessions](/superschematic/guides/users-and-sessions/#roles-and-capabilities)).
Over HTTP, a service that stands in for an end user holds none, so the
payment adapter calls the engine in-process (`engine.Call` in Go, the
`Deps` engine in TypeScript and Rust) with a principal of its own that
holds `payments.record`. A rules policy names that service, the staff
roles, and the runner, `<db>-engine-runner`, which moves claims for
`Timers` and `Reactions`: the first rule that matches decides, so the
runner's rule comes before the staff rules.

## A shop's types

A shop on the engine: inventory it counts and prices, a counter that
opens fifteen-minute pickup slots, checkouts that hold both while they
wait for payment, and refunds two staff sign off.

```ts
import { Finance, Generic, Temporal } from "superscalar";
import { behavior } from "@superschematic/schema";

@behavior("Stock", {
  price: { field: "priceCents", currency: "GBP" },
  lowAt: { field: "reorderAt" },
  maxPerHold: 99,
  permission: "stock.adjust",
})
export abstract class Inventory {
  sku: string;
  priceCents: Finance.Money;
  reorderAt?: Generic.Int64;
}

@behavior("Calendar", {
  timeZone: "Europe/London",
  hours: { mon: [["09:00", "12:30"], ["13:30", "17:30"]], tue: [["09:00", "17:30"]], sat: [["10:00", "16:00"]] },
  capacity: 4,
  slotMs: 900000,
  minMs: 900000,
  maxMs: 900000,
  bufferMs: { after: 300000 },
  leadMs: 7200000,
  horizonMs: 5184000000,
  permission: "calendar.manage",
})
export abstract class CollectionPoint {
  name: string;
}

export abstract class CheckoutLine {
  inventoryId: string;
  quantity: Generic.Int64;
}

@behavior("Workflow", {
  states: ["cart", "held", "placed", "expired", "cancelled"],
  transitions: [
    { from: "cart", to: "held" },
    { from: "held", to: "placed" },
    { from: "held", to: "expired" },
    { from: "held", to: "cancelled" },
    { from: "cart", to: "cancelled" },
  ],
  outcomes: { expired: "failure", cancelled: "neutral" },
})
@behavior("Links", { links: { counter: { schema: "CollectionPoint" } } })
@behavior("Holds", {
  holds: {
    lines: { each: { field: "lines", target: "inventoryId" }, schema: "Inventory", quantity: { field: "quantity" } },
    pickup: { link: "counter", interval: { start: "pickupAt", durationMs: 900000 } },
  },
  takeOn: ["held"],
  commitOn: ["placed"],
  releaseOn: ["expired", "cancelled"],
})
@behavior("Timers", {
  deadlines: {
    holdExpiry: { start: { enters: "held" }, afterMs: 900000, then: { transition: "expired" }, until: { leaves: "held" } },
    backstop: { start: { enters: "held" }, afterMs: 7200000, then: { transition: "expired" }, until: { leaves: "held" } },
  },
})
@behavior("Payments", {
  amount: { holds: true },
  require: { placed: "paid" },
  voidOn: ["expired", "cancelled"],
  afterRelease: "refund",
  refunds: { permission: "payments.refund" },
  record: { permission: "payments.record" },
})
@behavior("Reactions", { rules: [{ when: { changes: { field: "Payments.state", to: "paid" } }, then: { transition: "placed" } }] })
@behavior("Numbering", { format: "ORD-{yyyy}-{seq:5}", reset: "yearly" })
@behavior("StateRules", { states: { held: { frozen: ["lines", "pickupAt"] }, placed: { frozen: "all" } } })
export abstract class Checkout {
  lines: CheckoutLine[];
  pickupAt?: Temporal.DateTime;
}
```

`Refund` is under [refunds with Approvals](#refunds-with-approvals).
The engine's own tests compose the same types
(`internal/generator/servergen/testdata/engine/shop-commerce-db`), and
three servers, in Go, Rust and TypeScript, answer one script over them
alike ([the proof](#the-proof)).

## The supply contract

`Stock` and `Calendar` are supplies: they serve the instance operations
`holdFor`, `commitFor`, `releaseFor` and, for counted supply,
`returnFor`, and the read-only `checkHold`. `Holds` calls them by name
on the instance an item names, so it holds an extension's supply as it
holds the core's, and it never reads a supply's tables. Each takes the
same parameters:

```json
{ "holder": { "schema": "Checkout", "id": "c-1" }, "hold": "lines", "item": 0, "quantity": 2 }
{ "holder": { "schema": "Checkout", "id": "c-1" }, "hold": "pickup", "item": 0, "interval": { "startMs": 1793000000000, "endMs": 1793000900000 } }
```

- **The supply checks the claim.** A supply takes a writing call only
  for an item the holder's own `Holds.items` already shows, asking what
  the call asks, in the state the call moves the supply's record to.
  `Holds` writes the item's new state before it calls, in the same
  transaction, so a caller who invokes `holdFor` or `releaseFor` over
  HTTP with made-up parameters is refused `not_requested`. A supply's
  record of each item, by holder, hold and item, moves only forward, so a
  call made again changes nothing.
- **What does not fit is `unavailable`.** Its details name the supply,
  the hold and item, what was asked and what is free: `available` for a
  count, the `conflict`ing interval for time. A supply adds codes of its
  own with the same details (`outside_hours`, `too_soon`).
- **`checkHold`** answers whether `holdFor` would fit now, without the
  holder check or a write, counting what the claim's other items still
  to hold ask of the same instance (`also`), so two lines of one product
  are checked together.
- **An optional price.** A supply may name an own integer field as its
  unit price, with a currency. `holdFor` returns it and `Holds` keeps it
  on the item, so a line keeps the price it was held at, and the shopper
  never sets it.
- **A supply's delete** is refused while it holds anything (`held`): an
  item held, or committed time that has not ended.

An extension's supply implements the same operations with the crate's
`supply` module, which gives the parameter types, the holder check and
the `unavailable` details, so it joins `Holds` the way `Stock` and
`Calendar` do. `Stock` and `Calendar` declare the same operations and
conflict; five bikes rented by the hour are a `Calendar` with capacity 5.

## Stock

| | |
| --- | --- |
| Config | `permission` (required) for the operations that change the count; `price`: `{ field, currency }`, an own integer field and a code or an own `Finance.Currency` field; `lowAt`: a count or `{ field }`; `maxPerHold`; `backorder` (`false` when absent) |
| Fields | `onHand`; `held`; `available`, `onHand − held`, never below 0 unless `backorder`; `low`, volatile, true at or below `lowAt`, which a list filters on (`where: { "Stock.low": true }` is what to reorder) |
| Operations | `receiveStock({ quantity, reason, reference? })`, `adjustStock({ delta, reason })`, `countStock({ onHand, reason })`, each needing `permission`; the read-only `listStockMovements({ limit?, cursor? })`, newest first; the contract's operations |
| Vetoes | `unavailable` (`details.available`), `over_max`, `not_requested`, `below_held` (a count below what is held, unless the caller holds the permission's `.override`; an adjustment down past it unless `backorder`), `held` (a delete while holding) |

Every change of `onHand` is a row of an append-only ledger: its kind
(`receive`, `adjust`, `count`, `commit`, `return`), delta, what was on
hand after it, the reason and reference, the claim and item it was for,
who made it and when. `onHand` always equals the sum of the movements,
and `held` the sum of what the supply's records hold. A commit takes the
units out of `onHand` and `held`: they are sold. Every operation that
moves a count appends the instance's own event with `Stock.onHand`,
`held` and `available` in its diff, so a `Reactions` rule can hear
`Stock.available` reach 0.

## Calendar

| | |
| --- | --- |
| Config | `timeZone` (IANA); `hours`: by day `mon` to `sun`, `[["HH:MM", "HH:MM"], …]` in the zone; `capacity`: a count or `{ field }`; `slotMs`, the start grid from each range's opening; `minMs`, `maxMs`; `bufferMs: { before?, after? }`; `leadMs`, `horizonMs`, against the engine's clock; `permission`, for an instance's own hours and closures |
| Fields | `timeZone`, `hours` (the instance's own, or the config's), `closures` (those that have not ended, at most 100); all volatile |
| Operations | `setOpeningHours({ hours?, timeZone? })`, `addClosure({ startMs, endMs, reason, force? })`, `removeClosure({ closure })`; the read-only `listBookings({ startMs, endMs, limit?, cursor? })` and, schema-level, `findAvailability({ startMs, endMs, durationMs, ids?, where?, limit? })`; the contract's operations but `returnFor` |
| Vetoes | in `holdFor`'s order: `outside_hours`, `closed`, `off_grid`, `too_short`, `too_long`, `too_soon`, `too_far`, `unavailable` (the conflicting hold at the peak); `has_bookings` (a closure over held time unless forced with the permission's `.override`, a zone change while time is held); `window_too_long` (past 31 days); `no_closure`; `held` |

Capacity counts the most holds over any instant of the interval, each
with its buffers, so a long hold two short ones overlap at different
times is counted once. Local times become instants through `jiff` and
its bundled time zone database, so every server built from one lock
file computes the same slots: a wall time a clock change skips has no
slot, and a repeated one gives two. `findAvailability` lists free slots
across up to 100 instances, at most 500, soonest first:

```json
POST /schemas/CollectionPoint/operations/findAvailability
{ "startMs": 1793000000000, "endMs": 1793005400000, "durationMs": 900000, "ids": ["front"], "limit": 10 }
→ { "slots": [{ "id": "front", "startMs": 1793000000000, "endMs": 1793000900000, "remaining": 4 }, …] }
```

Two resources booked together, a stylist and a chair, are two calls the
caller intersects.

## Holds

| | |
| --- | --- |
| Config | `holds`: by name, a single-valued `link` of the type's Links, or `each: { field, target }` with `schema`, an own list whose elements name a supply's id; each with a `quantity` (a count or `{ field }`) or an `interval` (`{ start, end }` or `{ start, durationMs }`, date-time fields); `takeOn`, `commitOn`, `releaseOn`, `returnOn`: states of the type's Workflow (`takeOn` absent: at the create); `maxItems` (100) |
| Fields | `items`: each `{ hold, item, schema, id, quantity or startMs and endMs, state, unitPrice?, currency? }`; `state`, the least advanced item's (`none`, `pending`, `held`, `committed`, `released`, `returned`); `total`, `{ amount, currency }` ([totals](#totals-and-currency)) |
| Operations | `retakeHolds`, which takes again in a `takeOn` state after a supply refused or a new version added a hold; the read-only `listHolds` |
| Vetoes | `unavailable` (every item that does not fit, in `details.items`), `holds_missing` (a move into a `commitOn` state while an item is not held), `held_fields` (an update of what a hold reads while the claim holds), `committed` (a delete while committed time has not ended), `too_many_items`, `not_taking`, `no_supply` |

- **All or none.** Entering a `takeOn` state, its guard asks each
  supply's `checkHold` and refuses with every item that does not fit;
  then each item is held in a savepoint of the same transaction, so a
  claim holds every item or none. A cart in a state before `takeOn`
  shows its items `pending`: what it would hold.
- **Moving.** Entering a `commitOn` state commits each held item,
  a `releaseOn` state releases them, and a `returnOn` state returns
  committed units. `Holds` reads the status as the instance has it, so a
  `Timers` fire, a `Reactions` move and a caller's `transition` are
  caught alike. It has no release operation: a claim lets go by a move
  of its status, which runs its guards and leaves its event.
- **Deleting** a claim releases what it holds and leaves what it
  committed: units stay sold and time booked.

## Totals and currency

An amount is a whole number of a currency's minor units, 450 for £4.50,
and whatever holds one names its currency: an ISO 4217 code, as a
literal (`"GBP"`) the engine checks or an own field of superscalar's
`Finance.Currency`. Amounts of two currencies are never added.

`Holds.total` sums the priced items' unit prices times their quantities.
An item without a price, such as a pickup slot, adds nothing; with none
priced the total is `{ amount: 0, currency: null }`. It is null when the
priced items' currencies differ or the sum passes what a JSON number
holds exactly.

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
| Config | `amount`: `{ field }` or `{ holds: true }`; `currency` for an amount from a field; `capture`: `automatic` or `manual` with `captureOn`; `require`: `{ <state>: "authorized" or "paid" }`; `voidOn`; `afterRelease`: `refund` or `keep`; `refunds: { permission }`; `record: { permission }` (required: the adapter's); `refundOf: { link }` and `issueOn` on a refund instance |
| Fields | `due`, `authorized`, `captured`, `refunded`, `outstanding`, `overpaid`, `currency`, `state` (`unpaid`, `pending`, `authorized`, `paid`, `partiallyRefunded`, `refunded`), and the last twenty `attempts`; none volatile, so a rule hears `Payments.state` turn `paid` |
| Operations | `openPayment({ provider, idempotencyKey, returnUrl? })`, `capturePayment`, `voidPayment`, `refundPayment({ amount, reason, idempotencyKey })`; the adapter's `recordPaymentEvent` and schema-level `listPaymentActions({ provider })`; `refundFor` and `settleRefundFor`, which a refund instance and the instance it refunds call on each other |
| Vetoes | `nothing_due`, `total_unknown`, `amount_locked`, `currency_mismatch`, `over_capture`, `exceeds_refundable`, `unpaid`, `unauthorized`, `payment_in_progress`, `no_attempt`, `not_linked`, `key_reused`, `attempt_closed`, `not_configured` |

**The engine never calls a provider.** `openPayment` records an
attempt and an action for the provider, and returns the attempt and its
amount; the same key returns the same attempt. An adapter outside the
engine, code in your API, does what each action asks and reports back:

```
shopper ──▶ API ──in-process──▶ engine: openPayment → { attempt, amount, currency }
              │  provider: create a payment, Idempotency-Key = the action's id
              ▼
          provider ──hosted fields──▶ browser    (card data never reaches the shop)
          provider ──webhook──▶ API @webhook @hmacVerified ──▶ engine: recordPaymentEvent
          settle job ──▶ engine: listPaymentActions ──▶ provider: capture, void, refund
                                                    └─▶ engine: recordPaymentEvent
```

- **The webhook.** An operation marked `@webhook @hmacVerified({ provider })`
  ([webhooks](/superschematic/guides/api-routes/#webhooks)) checks the
  provider's signature before anything else, maps each event to
  `recordPaymentEvent({ provider, eventId, attempt or providerRef, kind,
  amount, currency, at })`, and answers quickly: 2xx for an event it does
  not know and for a veto such as `currency_mismatch`, which it logs, so
  the provider does not retry forever, and 503 for `busy`, so it does.
- **The settle job.** A [job](/superschematic/guides/stacks/#jobs),
  every minute, reads `listPaymentActions` for its provider, calls the
  provider for each with the action's id as the provider's idempotency
  key, and records the outcome. It also asks the provider about attempts
  stuck in `processing`. Two instances of the job may read the same
  action; the provider's idempotency key gives the second the first's
  result, and recording it again changes nothing.
- **Idempotency.** An event applies once per `(provider, eventId)` in the
  namespace: a delivery repeated changes and writes nothing, and the same
  id with other content is `key_reused`. An attempt's status only moves
  forward (`open < requiresAction < processing < failed < authorized <
  cancelled < captured`), so a `processing` that arrives after
  `captured` is recorded and ignored, and providers' delivery order does
  not matter.
- **The adapter's principal.** The webhook and the job call the engine
  in-process with the adapter's principal, which holds
  `record.permission` and which the engine policy lets write the
  schemas it records on, a refund's `settleRefundFor` included. End
  users never hold `payments.record`.
- **Secrets.** The provider's API key and signing secret are `Secret<T>`
  fields of the API's config ([stacks](/superschematic/guides/stacks/#wire-the-services)),
  kept in each platform's secret store, never in the engine's config,
  its document or its log.
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

**Paying twice.** A provider may still finish a payment it reported
failed. `openPayment` gives each earlier failed attempt a `void` action,
and a capture that pays what is due voids the other open ones. A capture
that arrives anyway is counted: what it brings past what is due is
`Payments.overpaid`, which Payments asks back with a refund, leaving the
state `paid`.

### Refunds with Approvals

A shop without sign-off refunds with `refundPayment`, which needs
`refunds.permission` and is refused `exceeds_refundable` past what one
captured attempt has left. A refund that needs two staff is an instance
of its own:

```ts
@behavior("Workflow", {
  states: ["requested", "approved", "issued", "rejected", "failed"],
  transitions: [
    { from: "requested", to: "approved" },
    { from: "requested", to: "rejected" },
    { from: "approved", to: "issued" },
    { from: "approved", to: "failed" },
  ],
})
@behavior("Approvals", { gates: { approved: { count: 2, permission: "refunds.approve" } } })
@behavior("Links", { links: { order: { schema: "Checkout", required: true } } })
@behavior("Numbering", { format: "RF-{yyyy}-{seq:5}", reset: "yearly" })
@behavior("StateRules", { states: { requested: { required: ["amountCents", "reason"] }, approved: { frozen: "all" } } })
@behavior("Payments", {
  amount: { field: "amountCents" },
  currency: "GBP",
  refundOf: { link: "order" },
  issueOn: ["approved"],
  record: { permission: "payments.record" },
})
@behavior("Reactions", { rules: [{ when: { changes: { field: "Payments.state", to: "refunded" } }, then: { transition: "issued" } }] })
export abstract class Refund {
  amountCents: Finance.Money;
  reason: string;
}
```

1. Two holders of `refunds.approve` sign it off; its creator may not
   (`creator_cannot_sign`), and a move into `approved` with one sign-off
   is `approvals_missing`
   ([Approvals](/superschematic/guides/engine-behaviors/#approvals)).
2. Entering `approved` invokes `refundFor` on the linked checkout, which
   checks the link and the amount and records the refund and its action.
   One approval pays one refund, whatever key a caller who reaches
   `refundFor` makes up.
3. The settle job sends the refund to the provider, and records its
   outcome on the checkout (`kind: "refunded"`, with the refund's id),
   which invokes `settleRefundFor` back on the refund, as the adapter: its
   `record.permission` must be the checkout's.
4. The refund's `Payments.state` turns `refunded`, and the runner's
   `Reactions` rule moves it to `issued`.

## Expiry with Timers

`Holds` has no expiry of its own. A `Timers` deadline moves the claim
into a `releaseOn` state, as `holdExpiry` does above, and the release
gives the units and the slot back. `Holds`' guidance names a `takeOn`
state no deadline leaves, which a quote held until withdrawn may want.

- The runner fires a due deadline within `sweepMs` (5 s by default)
  ([the runner](/superschematic/guides/engine-behaviors/#the-runner)).
  While no runner runs nothing expires, and when one starts every overdue
  deadline fires; a stack runs one runner per engine database.
- **The late payment.** A shopper pays at 14:59:58 and the hold expires
  at 15:00. The attempt is `processing`, so `Payments` vetoes the move,
  and `Timers` marks the deadline fired with the outcome `vetoed`; the
  checkout stays held. When the capture is recorded, a `Reactions` rule
  on `Payments.state` places it. A second, longer deadline, `backstop`,
  lets go of a claim whose payment never reports.

## MCP tools and agents

Every operation is an MCP tool of the type
([MCP tools](/superschematic/reference/mcp-tools/#the-engine)), so an
agent can sell and book. Two settings decide what it may do unasked:

- **`tools.invocation`.** The core's declarations name no invocation
  policy, so they register under any distribution's. A build whose
  engine types compose `Payments` or `Stock` is refused unless the engine
  database's `outputs.engine.tools.invocation` names one for each
  operation that moves money or stock: `Payments.openPayment`,
  `capturePayment`, `voidPayment`, `refundPayment` and `refundFor`, and
  `Stock.receiveStock`, `adjustStock` and `countStock`. `ask` makes an
  agent's client confirm each call. The choice is the deployment's: the
  example below asks for all eight, and the acme shop asks for the five
  that move a shopper's money and for adjusting and counting stock, but
  lets an agent receive stock unasked, since a receipt is routine and
  another movement reverses it.
- **`tools.hidden`.** The engine document hides Payments' service
  operations wherever it is composed: `listPaymentActions` and
  `recordPaymentEvent`, which the adapter calls, and `settleRefundFor`.
  `tools.hidden` adds others, such as Stock's ledger. A hidden operation
  still runs through the engine's API.

```yaml
outputs:
  engine:
    tools:
      invocation:
        Payments.openPayment: ask
        Payments.capturePayment: ask
        Payments.voidPayment: ask
        Payments.refundPayment: ask
        Payments.refundFor: ask
        Stock.receiveStock: ask
        Stock.adjustStock: ask
        Stock.countStock: ask
      hidden:
        - Stock.listStockMovements
```

## Concurrency and throughput

Every write in a namespace takes its writer lock (D58), so the check of
what is free and the hold that takes it cannot race, across replicas and
languages. Three servers, in Go, Rust and TypeScript, on one Postgres,
given 1,000 checkouts at once each asking for one of the last three
units, hold exactly three; the rest are `unavailable`. 1,000 bookings of
one slot of capacity 1 hold exactly one.

The same lock makes a namespace's commerce writes one queue: a flash
sale on one product waits with every other write in the namespace.
Measured on a laptop, three servers and 24 clients against Postgres in a
local VM, a checkout of one item held at about 12 a second, of ten items
51 holds a second, and of fifty 75 holds a second, and 1,000 carts list
in about 10 seconds in pages of 100. The time is in the statements each
move runs under the lock, about 400 for a checkout of ten items, each a
round trip to the database, rather than in the database's own work; a
database closer to the servers is faster. A
move that waits in the queue past a server's 30-second engine route
timeout answers 504 `gateway_timeout`, which a client asks again. Give
each shop a namespace of its own.

## Personal data

`Payments` keeps holder references, amounts, the provider's reference
and a card's brand and last four digits; `Holds` and `Calendar` keep
references and intervals. A claim's own fields may hold more, a
delivery address or a phone number, and the event log keeps each
change's diff until retention prunes it. Turn the engine database's
retention on for a commerce namespace
([retention](https://github.com/parable-work/superschematic/blob/main/runtime/engine/README.md#retention)),
and keep the payer's identity with the provider.

## The acme shop

`examples/acme-shop` is the worked example. Its shop database's engine
types are `Inventory` (`Stock`), `CollectionPoint` (`Calendar`),
`Checkout` (`Workflow`, `Links`, `Holds`, `Timers`, `Payments`,
`Reactions`, `Numbering` and `StateRules`) and `Refund` (`Workflow`, two
`Approvals` sign-offs, `Links`, `Numbering`, `StateRules`, `Payments`
with `refundOf`, and `Reactions`). Every server, job, worker and runner
on the database opens its engine, and each service plays one part:

- `shop-cart`, a TypeScript API, is the cart: a shopper's cart is a
  `Checkout` in `cart`, and the pickup slots are `CollectionPoint`'s
  `findAvailability`;
- `shop-orders`, in Go, moves a checkout from `cart` to `held`
  (`startCheckout`), which holds every line's units and the slot or none;
  opens its payment and makes the intent at the provider (`payCheckout`);
  receives the provider's events at `paymentEvents`, a webhook verified
  by `@hmacVerified` (a `t=…,v1=…` HMAC with a five-minute tolerance),
  recording each under the event's id;
  and asks for refunds (`requestRefund`). Its job `SettlePayments` makes
  the payment actions the engine holds at the provider, each with the
  action's id as its idempotency key, asks after attempts left
  processing, and writes the `Order` row of each placed checkout;
- the runner's `Reactions` rule places a checkout once its capture is
  recorded, which commits its units and slot. The `Order` row, unique by
  `checkoutId`, is written when `shop-orders` next looks: the webhook,
  `getCheckout` or `SettlePayments`. `shop-orders` then tells `shop-stock`
  what the checkout sold;
- `shop-stock`, in Rust, keeps the stock over `Inventory`: staff count a
  product's shelf and book deliveries in, and `stockSold`, which only
  `shop-orders` may call (`@requireService`), enqueues `StockLow` for a
  product a sale left low (`Stock.low`). Its job `OrderRestocks` files a
  restock request for each `Inventory` the engine lists low;
- a `Refund` of a placed checkout is approved by two members of staff
  who hold `refunds.approve`; `SettlePayments` makes it at the provider,
  and the runner's rule moves it to `issued` once refunded;
- `acmepay`, a fake provider in Go, takes test tokens only and refuses
  card numbers, signs its webhooks and honours idempotency keys. `stack
  dev` and the tests pay through it without a real provider: it runs
  beside `stack dev` on `127.0.0.1:4250`, and in-process in the tests.

The database's policy is ordered rules, the first that matches deciding:
`shop-orders` reads and writes every type, `shop-cart` writes checkouts
and reads the stock and the counters, `shop-stock` keeps the stock, and
any other service is refused; then the runner (`shop-db-engine-runner`)
holds each type by its name, a rule that comes before the staff's, since
the runner reading a checkout would otherwise meet a staff rule first;
then staff by their roles: `refunds.approve`, `orders.any`, `stock.write`
and `calendar.manage`.

`placeOrder` still takes an order in one step, holding no stock and
taking no payment, for the shop's Rust server, which answers the
checkout routes and the webhook 501, and its Topcoat app, which keeps
the back office below but sells nothing through its engine. The
[README](https://github.com/parable-work/superschematic/blob/main/examples/acme-shop/README.md#commerce)
walks through a sale, and D70's amendment, "acme-shop sells, books and
refunds through the engine", records why each piece runs where it does.

## The back office

A [Topcoat app](/superschematic/guides/topcoat/#engine-pages) over an API
of the engine database pages its engine's types, so staff work the
shop's stock, pickup points, checkouts and refunds from a browser, each
call made as the signed-in member of staff, whom the database's policy
admits by their permissions:

- an `Inventory`'s page shows on hand, held, available and low, the
  receive, adjust and count forms, and the movements, newest first. A
  form sends the page's `expectedSeq`, so a second click is 409, not a
  second delivery; <!-- verify: fix-eng -->
- a `CollectionPoint`'s shows its time zone, its opening hours as a week,
  its closures and the bookings of a range of dates;
- a `Checkout`'s shows its holds, each item's unit price and the total as
  money, `GBP 39.98`, or "Mixed currencies" when the total is null, and its
  payment: what is due, captured and refunded, its state and its
  attempts. Since `Refund` refunds it through `Approvals`, the payment
  links to the refunds rather than holding a refund form;
- a `Refund`'s shows its sign-offs against the two its `approved` gate
  needs, a sign-off form for a member of staff who holds
  `refunds.approve`, and the moves. Moving it to `approved` with one
  sign-off is 409 `approvals_missing`, with the engine's message on the
  form.

A type that refunds itself directly gets `refundPayment`'s form, whose
idempotency key is made as the form renders, so a form sent twice
refunds once. <!-- verify: fix-eng -->

acme's Topcoat app has this back office at `/console/engine`, and the
shop's users and roles at `/console/users` and `/console/roles`. Its
engine is a SQLite file of its own and has no payment adapter, so it
sells nothing: its checkouts are those its tests write, as the stack's
`shop-orders` and acmepay's adapter would. `topcoat/tests/back_office.rs`
seeds an anvil, the front counter, Grace's checkout of two anvils, held
and paid, and a refund of GBP 10.00. Ada receives five anvils through
the Stock panel, and Grace is refused (403). Ada signs the refund off,
moving it is refused, Ada adds Katherine through the users pages and
grants her the `refunds` role, Katherine signs it off, and Ada moves it
to `approved`. Once the adapter reports the refund paid, its payment and
the checkout's show it refunded. The app runs no runner, so the refund
stays `approved` rather than moving on to `issued`.

The app's demo staff, `ada@example.com`, holds the roles the policy
names: `staff` (`orders.any`), `refunds` (`refunds.approve`), `stock`
(`stock.write`), `collection-points` (`calendar.manage`) and `admin`
(`identity.users.*`, `identity.roles.*`). She signs in at the app's own
`/sign-in`, which the console links to, and signs out at
`/console/sign-out`; the console's own `/console/sign-in` is served but
linked from nowhere.

## The proof

`TestCommerceServersAnswerOneScriptAlike`
(`internal/generator/servergen`) runs one script against a Go, a Rust
and a TypeScript server over one Postgres: stock received, a cart that
holds units and a pickup slot, `total_unknown` while it is pending, a
payment through the provider's events (a repeat, a late one and one in
another currency among them), placing, a refund two approvers sign off,
expiry and the late payment through the runner, and each server reading
what the others wrote. The three transcripts are identical once the
run's own ids and times are written as symbols.
`TestCommerceServersOversellNothing` is the race above, and
`TestCommerceHoldsLoad`, which runs with
`SUPERSCHEMATIC_COMMERCE_LOAD=1`, the measurement.

## Where to go next

- The package's
  [README](https://github.com/parable-work/superschematic/blob/main/runtime/engine-commerce/README.md):
  every refusal, event and config change, and the supply contract for
  your own supply.
- [Engine behaviors](/superschematic/guides/engine-behaviors/): `Timers`,
  `Approvals`, `Reactions`, `Numbering` and `StateRules`, which a shop
  composes beside these.
- The acme shop's
  [README](https://github.com/parable-work/superschematic/blob/main/examples/acme-shop/README.md):
  a cart, an orders API with a payment adapter and its webhook, a stock
  API and a fake provider, in three languages.
