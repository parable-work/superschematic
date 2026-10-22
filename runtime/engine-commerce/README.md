# Engine commerce

The engine's commerce package (D70 in `docs/DECISIONS.md`): its crate
`rust/`, `superschematic-engine-commerce`, and the npm package
`@superschematic/engine-commerce` (`typescript/`). Four behaviors sell
units a supply counts and book time a supply opens, to claims that hold
both while they wait for payment:

- `Stock`, units a supply counts, with a ledger of every movement;
- `Calendar`, time a supply opens: hours in a time zone, a capacity, a
  grid, lead time and horizon, closures and free slots;
- `Holds`, one claim's holds on any supply, tied to its Workflow;
- `Payments`, what an instance owes and the payments and refunds that
  settle it, through an adapter outside the engine.

The core declares them under bare names (`internal/registry/behaviors`),
with the package `@superschematic/engine-commerce`, so every binary's
meta-schema admits a schema that composes them, and `BehaviorConfigs` in
`@superschematic/schema` types their configs. They are Rust, on the
engine's plug-in API alone, as the work queue's are
(`runtime/engine-workqueue/README.md`). The npm package holds their names
(`commerceBehaviors`, each a `CommerceBehavior`), the types of their
configs and records (`StockRecord`, `HoldsRecord`, `PaymentsRecord`,
`PaymentAction`, `CalendarSlot` and the rest) and their constants
(`DEFAULT_MAX_ITEMS`, `MAX_WINDOW_MS`).

The guide is
[Selling and booking with the engine](../../docs/src/content/docs/guides/commerce.md);
this file is the reference for every refusal, event and config change.

```ts
import { openEngine } from '@superschematic/engine';
import { commerceBehaviors, type PaymentsRecord } from '@superschematic/engine-commerce';

const engine = openEngine({ path: 'shop.db', policy, metaSchema, behaviors: commerceBehaviors, runner: { principal: runner } });
engine.runner.start();
engine.instances.invoke(staff, 'Inventory', 'tea', 'receiveStock', { quantity: 3, reason: 'delivery' });
engine.instances.invoke(shopper, 'Checkout', 'c-1', 'transition', { to: 'held' });
const { attempt } = engine.instances.invoke(shopper, 'Checkout', 'c-1', 'openPayment', { provider: 'acmepay', idempotencyKey: 'c-1-pay' }) as { attempt: string };
engine.instances.invoke(adapter, 'Checkout', 'c-1', 'recordPaymentEvent', { provider: 'acmepay', eventId: 'evt_1', attempt, kind: 'captured', amount: 900, currency: 'GBP', at: Date.now() });
(engine.instances.get(shopper, 'Checkout', 'c-1')?.behaviors.Payments as unknown as PaymentsRecord).state;   // 'paid'
```

## Registering the behaviors

- **TypeScript.** The core's Node-API addon (`runtime/engine/rust/addon`)
  is built with the crate, after the work queue's, and a release ships it
  prebuilt. A deployment names the behaviors in the engine's `behaviors`
  option, all four with `commerceBehaviors` or only those it uses; a
  generated TypeScript server names the ones its engine types compose.
- **Rust and Go.** The project's engine crate, which the `projectengine`
  generator writes when a DB schema's types compose a commerce behavior,
  depends on the crate at `[paths] engine_commerce_rust` and registers
  `superschematic_engine_commerce::commerce()` in its `behaviors()`. A
  Rust server opens the engine with it, and a Go server links the
  crate's archive (its `ffi/` member) in the core's place. The core's Go
  archive does not link the crate, whose time zone database would
  otherwise be in every Go server.
- **By hand.** `commerce()` gives every behavior in `NAMES`' order
  (`Stock`, `Calendar`, `Holds`, `Payments`), after the core's;
  `behavior(name)` gives one.

```rust
let mut options = EngineOptions::sqlite("shop.db");
options.behaviors = BehaviorSet::new().extend(superschematic_engine_commerce::commerce());
let engine = Engine::open(options)?;
```

An engine refuses a schema that composes one it does not run, as for any
behavior. Each behavior embeds the copy of its declaration in
`rust/declarations`, which `make behaviors` writes from the core's
(`superschematic behaviors --package @superschematic/engine-commerce`)
and `make behaviors-check` checks. Guidance from each config (the
describe document and every tool's `_meta`) is in `rust/src/guidance/`
and `rust/src/calendar/guidance.rs` and `rust/src/payments/guidance.rs`.

Three rules hold throughout, beside D56's: times are epoch milliseconds
and durations integer milliseconds, opening hours `"HH:MM"` wall times;
every refusal is a veto with a declared code but a missing permission,
which is `forbidden`; and every table is the behavior's own, with the
same SQL on SQLite and Postgres.

## Money

An amount is a whole number of a currency's minor units, and whatever
holds one names its currency: an ISO 4217 code, a literal in the config
(`"GBP"`), which the engine checks against superscalar's
`Finance.Currency` table (`rust/src/currency.rs`), or an own field of
the type that is a `Finance.Currency`. Amounts of two currencies are
never added, and no amount passes what a JSON number holds exactly
(2^53 − 1).

## The supply contract

`rust/src/supply.rs`. A supply behavior (`Stock`, `Calendar`, or an
extension's) serves the instance operations `holdFor`, `commitFor`,
`releaseFor` and, for counted supply, `returnFor`, which write, and
`checkHold`, which only reads. `Holds` invokes them by name on the
instance an item names, so an extension's supply is held as the core's
are, and it never reads a supply's tables.

```json
{ "holder": { "schema": "Checkout", "id": "c-1" }, "hold": "lines", "item": 0, "quantity": 2 }
{ "holder": { "schema": "Booking", "id": "b-1" }, "hold": "slot", "item": 0, "interval": { "startMs": 1793000000000, "endMs": 1793000900000 } }
```

`item` is the element's index in an `each` hold's list, 0 for a `link`
hold. An interval lies from 1900 to 9000 (`EARLIEST_MS`, `LATEST_MS`).
`checkHold` also takes `also`: what the claim's other items still to hold
ask of the same instance, a count `Stock` subtracts from what is
available and intervals `Calendar` counts against capacity.

- **The holder check.** A writing call is taken only from the claim
  itself: the supply reads the holder's `Holds.items` (`check_holder`)
  and refuses the call `not_requested` unless an item of it, by hold and
  index, names this supply, asks what the call asks, and is already in
  the state the call moves the supply's record to. `Holds` writes an
  item's new state before it calls, in the same transaction.
- **The record.** A supply keeps a row per holder, hold and item
  (`rows_table`), which a call moves only forward: one whose row is
  already in the state it asks changes and writes nothing, and one whose
  row is in no state the call moves from is `not_requested`.
- **A gone holder.** A claim's delete leaves its afterChange to release:
  a supply then takes `releaseFor` alone, for what its record asks. What
  was held is released, what was committed stays (units sold, time
  booked), and the record is retired: a `retired` key column, 0 while the
  holder lives, keeps its state and frees the holder's id, so a claim
  created again with it holds afresh. Nothing gives a gone holder's
  committed units back.
- **`unavailable`.** What does not fit: `details` name the supply, the
  hold and item, what was asked, and what is free, a count `available`
  or the `conflict`ing interval. A supply's own codes carry the same
  members. `checkHold` answers with the refusal rather than vetoing, so a
  claim's guard lists every item that does not fit.
- **A supply's delete** is refused `held` while it holds anything (an
  item held, or committed time that has not ended).
- **A price.** A supply may name an own integer field as its unit price,
  with a currency; `holdFor` returns it and Holds keeps it on the item.

An extension's supply implements the operations with `SupplyCall`,
`begin`, `finish`, `rows_table`, `Unavailable` and `holding`, and
declares the same operation names. Two supplies on one type would
declare the same names, so `Stock` and `Calendar` conflict.

## Stock

`rust/src/stock.rs`. Units a supply counts: an inventory record per
product, a ticket type, a coupon's redemptions.

| | |
| --- | --- |
| Config | `permission` (required); `price: { field, currency }`; `lowAt`: a count or `{ field }`; `maxPerHold`; `backorder` (`false`) |
| Fields | `onHand`; `held`; `available`, `onHand − held`, never below 0 unless `backorder`; `low`, volatile, true while `available` is at or below `lowAt` |
| Operations | `receiveStock({ quantity, reason, reference? })`, `adjustStock({ delta, reason })`, `countStock({ onHand, reason })`, each needing `permission`; read-only `listStockMovements({ limit?, cursor? })`; the contract's |
| Vetoes | `unavailable`, `over_max`, `not_requested`, `below_held`, `held` |

- **The ledger.** Every change of what is on hand is a row of an
  append-only movement table: its kind (`receive`, `adjust` and `count`
  by a caller with the permission, `commit` and `return` by a claim), its
  delta, what was on hand after it, its reason and reference, the claim
  and item it was for, who made it and when. `on_hand`, a column of the
  instance, always equals the sum of the movements' deltas, and `held`
  the sum of the quantities the supply's records hold. `listStockMovements`
  pages them newest first by D60's cursor.
- **The contract.** `holdFor` holds what fits what is available
  (`unavailable`, with `available`) within `maxPerHold` (`over_max`), and
  returns the unit price; `commitFor` takes the units out of on hand and
  held (sold), a `commit` movement, and without `backorder` is
  `unavailable` while that would leave fewer than 0 on hand; `releaseFor`
  frees them; `returnFor` puts back what the item committed, a `return`
  movement. With `backorder` a hold may take more than is available.
- **Counting.** An adjustment down that leaves fewer on hand than is held
  is `below_held` unless `backorder`, and one below 0 always; one up is
  taken even while it leaves too few. A count below what is held is
  `below_held` unless the caller holds the permission's `.override`,
  which under the default dotted permissions the permission covers. An
  adjustment of 0 and a count of what is on hand change nothing and
  record no movement.
- **`low`** filters a list (`where: { "Stock.low": true }`) through a
  column each write sets: after a version changes a literal `lowAt` the
  filter answers by the old one for an instance until its next write,
  while the field answers by the new one at once.
- **Events.** Every operation that moves a count appends the instance's
  own event, with `Stock.onHand`, `held` and `available` in its diff.
- **Delete.** Refused while the instance holds units for a claim
  (`held`); what claims committed is sold, and its records go with the
  instance.
- **configChange.** The price field, `lowAt`, `maxPerHold` and the
  permission may change; `backorder` turns off only while no instance is
  below zero or holds more than it has on hand. Stock joins a schema
  that has instances, each at zero, and leaves only one with none.

## Calendar

`rust/src/calendar/`. Time a supply opens, on a bookable resource: a
stylist, a chair, a room, a collection counter, a pool of bikes.

| | |
| --- | --- |
| Config | `timeZone` (IANA); `hours`, by day `mon` to `sun`, lists of `["HH:MM", "HH:MM"]`; `capacity`, a count or `{ field }`; `slotMs` (any whole minute when absent); `minMs` (`slotMs` or a minute), `maxMs` (a day); `bufferMs: { before?, after? }`; `leadMs`, `horizonMs`; `permission` |
| Fields | `timeZone`, `hours`, `closures` (the first 100 that have not ended); all volatile |
| Operations | `setOpeningHours({ hours?, timeZone? })`, `addClosure({ startMs, endMs, reason, force? })`, `removeClosure({ closure })`, each needing `permission`; read-only `listBookings({ startMs, endMs, limit?, cursor? })`; schema-level, read-only `findAvailability({ startMs, endMs, durationMs, ids?, where?, limit? })`; the contract's but `returnFor` |
| Vetoes | `not_requested`, `unavailable`, `held`, `outside_hours`, `closed`, `off_grid`, `too_short`, `too_long`, `too_soon`, `too_far`, `has_bookings`, `window_too_long`, `no_closure` |

- **`holdFor`'s checks, in order.** The interval lies inside the opening
  hours (`outside_hours`) and overlaps no closure (`closed`), reading only
  its first `maxMs` and a minute, so an interval of centuries costs what
  the longest hold does; it starts on the grid (`off_grid`), a whole
  number of `slotMs` after the opening time of its range, in wall time;
  its length is within the bounds (`too_short`, `too_long`); it starts
  no sooner than the lead time (`too_soon`) and no later than the horizon
  (`too_far`) from now; and the most holds over any instant of it, each
  with its buffers, stay below capacity (`unavailable`, with the hold
  that conflicts at the peak). Each code carries `unavailable`'s details
  with what it turns on (`timeZone`, `closure`, `slotMs`, `minMs`,
  `maxMs`, `earliestMs`, `latestMs`).
- **Time zones.** Local times become instants through `jiff` 0.2 with
  only `std` and `tzdb-bundle-always`: the IANA database is in the
  binary, and no host zone file is read, so every server built from one
  lock file computes the same slots. A wall time a change skips has no
  slot; a repeated one gives two. A hold's length is elapsed time.
- **`findAvailability`** lists the slots of a duration that would fit,
  soonest first, at most 500, over a window of at most 31 days
  (`window_too_long`), across at most 100 instances by id (`truncated`
  when more match); `where` takes the type's own fields that hold a
  string, a number or a boolean. One sweep of each instance's sorted
  holds jumps past a full stretch, a closure or a closed one, and stops
  at `MAX_FIND_WORK` (two million), answering `truncated`.
- **`listBookings`** pages an instance's holds over such a window, a
  deleted claim's committed time among them, marked `retired`.
- **Closures.** A closure over held time is `has_bookings` unless forced
  with the permission's `.override`; the bookings stay, for the
  business to move.
- **Storage.** The instance's own `hours` and `time_zone`, null for the
  config's, and a count of its closures; the contract's record table,
  whose index on (instance, start) serves the overlap read; `closures`.
- **Delete.** Refused while the instance holds time (`held`): an item
  held, or committed time that has not ended.
- **configChange.** Hours, the grid, buffers, lead time and horizon may
  change and apply to holds made after; `capacity` falls no lower than
  the most holds that have not ended overlap on an instance; `timeZone`
  changes only while no hold has future time, and an instance's own
  through `setOpeningHours` only while it holds none (`has_bookings`).
  An update that lowers a capacity field below the instance's peak is
  `has_bookings`. Calendar joins a schema that has instances and leaves
  only one with none.

## Holds

`rust/src/holds.rs`. One claim's holds on any supply, tied to its
Workflow: a checkout, a booking, a rental. It requires `Workflow`.

| | |
| --- | --- |
| Config | `holds`: by name, `{ link }` (a single-valued link of the type's Links, one item) or `{ each: { field, target }, schema }` (an own list, one item per element); each with `quantity` (a count or `{ field }`) or `interval` (`{ start, end }` or `{ start, durationMs }`, date-time fields); `takeOn` (absent: at the create), `commitOn`, `releaseOn`, `returnOn`; `maxItems` (100) |
| Fields | `items`, each `{ hold, item, schema, id, quantity or startMs and endMs, state, unitPrice?, currency? }`; `state`; `total` |
| Operations | `retakeHolds`; read-only `listHolds` |
| Vetoes | `unavailable`, `holds_missing`, `held_fields`, `committed`, `too_many_items`, `not_taking`, `no_supply` |

- **Taking.** A create into a `takeOn` state, a transition into one, and
  an update in one that changes what the items ask while the claim holds
  nothing take the holds. The guard asks each supply's `checkHold` about
  each item still to hold, with `also`, and vetoes `unavailable`, its
  `details.items` listing every item that does not fit; an interval
  outside 1900 to 9000 is listed without asking. afterChange then holds
  each item through `holdFor`, each call a savepoint of the call's
  transaction, so the claim holds every item or none, and keeps the unit
  price and currency `holdFor` returns. `retakeHolds` takes again in a
  `takeOn` state while an item is not held (`not_taking` otherwise).
- **Moving.** afterChange reads the status from the instance as it is,
  so a Timers fire, a Lease expiry and a Reactions move are caught as a
  caller's transition is: a `commitOn` state commits each held item, a
  `releaseOn` state releases each pending or held one, a `returnOn` state
  returns each committed item of counted supply. A move into a
  `commitOn` state while an item is neither held nor committed is
  `holds_missing`. There is no release operation: a claim lets go by a
  move of its status.
- **States only advance**: `pending → held → committed → returned`, or
  `pending | held → released`. A supply records each item once, so
  `parse_config` refuses a transition from a `releaseOn` or `returnOn`
  state into a `takeOn` state, and from a `takeOn` state to itself, which
  would start its expiry again.
- **While the claim holds** (an item held or committed), an update that
  changes a field a hold reads, and a Links move of a `link` hold's link,
  are `held_fields`.
- **Delete.** Releases what the claim holds; what it committed stays.
  Refused `committed` while the claim holds committed time that has not
  ended. The claim records a reference to each supply it holds from,
  which hears the supply's delete. More items than `maxItems` is
  `too_many_items`; an item whose supply does not exist or does not serve
  the contract is `no_supply`.
- **Fields.** `items` lists every item the claim has taken, as it
  stands, and every item its fields and links ask that it has not, as
  `pending`, by hold name and index. `state` is the least advanced
  item's, `none` before any. `total` is `{ amount, currency }`, the sum of
  the priced items' unit prices times their quantities: an item with no
  price adds nothing, none priced is `{ amount: 0, currency: null }`, and
  it is null only when the priced items' currencies differ or the sum
  passes what a JSON number holds. None is volatile: a Reactions rule
  hears `Holds.state` turn `committed`.
- **Expiry** is a Timers deadline the schema writes into a `releaseOn`
  state; the guidance names a `takeOn` state no deadline leaves.
- **configChange.** Holds, states and `maxItems` may change. Holds joins
  a schema that has instances, whose items are taken at their next entry
  into a `takeOn` state or by `retakeHolds`, and leaves only one with
  none.

## Payments

`rust/src/payments.rs`. What an instance owes and how it is settled,
without the engine ever calling a payment provider.

| | |
| --- | --- |
| Config | `amount`: `{ field }` or `{ holds: true }` (the type lists Payments after Holds); `currency`: a code or `{ field }`, for an amount from a field; `capture`: `automatic` (default) or `manual` with `captureOn`; `require`: `{ <state>: "authorized" or "paid" }`, no initial state; `voidOn`; `afterRelease`: `refund` or `keep` (default); `refunds: { permission }`; `record: { permission }` (required); `refundOf: { link }` and `issueOn`, on a refund instance |
| Fields | `due`, `authorized`, `captured`, `refunded`, `outstanding` (`due − captured`, never below 0), `overpaid`, `currency`, `state` (`unpaid`, `pending`, `authorized`, `paid`, `partiallyRefunded`, `refunded`), `attempts` (the last 20, newest first); none volatile |
| Operations | `openPayment({ provider, idempotencyKey, returnUrl? })`; `recordPaymentEvent({ provider, eventId, attempt? or providerRef?, refund?, kind, amount, currency, at, brand?, last4?, metadata? })`; `capturePayment({ attempt, amount? })`; `voidPayment({ attempt })`; `refundPayment({ amount, reason, idempotencyKey })`; schema-level, read-only `listPaymentActions({ provider, limit?, cursor? })`; `refundFor` and `settleRefundFor`, the refund pair |
| Vetoes | `nothing_due`, `total_unknown`, `amount_locked`, `currency_mismatch`, `over_capture`, `exceeds_refundable`, `unpaid`, `unauthorized`, `payment_in_progress`, `no_attempt`, `not_linked`, `key_reused`, `attempt_closed`, `not_configured` |

- **What is owed.** From an own integer field in the config's currency,
  or from `Holds.total`, which carries its own. From holds it fails
  closed: while an item is pending, or `Holds.total` is null over priced
  items, `due` reads 0 and `openPayment` and a `require` move refuse
  `total_unknown`. A claim whose held items carry no price owes 0.
- **What is recorded**, in Payments' own tables: an attempt per payment
  opened, with its provider, the provider's reference once reported, its
  amount and currency, what was due when it opened, its status, what is
  authorized and captured, and a card's brand and last four digits; an
  action per thing the adapter is to do (`create`, `capture`, `void`,
  `refund`), waiting until an event for it is recorded; a refund per
  refund asked (`requested`, `sent`, `succeeded`, `failed`); on a refund
  instance, each refund it asked (an issue); and every provider event
  recorded, by provider and event id, with the normalised fields only.
  Ids are `pay_`, `act_` and `rf_` before a version 4 UUID, which the
  adapter sends its provider as an idempotency key.
- **Opening.** `openPayment` records an attempt and its `create` action
  and returns `{ attempt, amount, currency, provider, status }`; the same
  key returns the same attempt, and the key with other parameters is
  `key_reused`. No second attempt opens while one is in flight
  (`nothing_due`): a payer who switches method voids the first. While an
  attempt is in flight, what is due does not change: a change of the
  amount field, or of `Holds.total`, is `amount_locked`.
- **The adapter.** `listPaymentActions` and `recordPaymentEvent` need
  `record.permission`. An event applies once per `(provider, eventId)` in
  the namespace: a repeat changes and writes nothing (`applied: false`),
  and the same id with other content, or on another instance, is
  `key_reused`. An attempt's status only moves forward, by the rank
  `open < requiresAction < processing < failed < authorized < cancelled
  < captured`, so a late event changes nothing, a payment that succeeds
  after a failed try is caught, and money a provider captured is counted
  whatever came before. An event in another currency is
  `currency_mismatch`; a capture past what is authorized, or with
  nothing authorized past the attempt's amount, is `over_capture`.
- **Paying twice.** `openPayment` gives each earlier failed attempt a
  `void` action, and a capture that pays what is due voids the other
  open attempts. A capture that arrives anyway is counted; what it brings
  past what is due is `overpaid`, asked back with a refund keyed
  `overpaid:<eventId>`, which leaves the state `paid`. `refundPayment`
  refuses the `overpaid:` and `release:` keys, which are Payments' own.
- **The Workflow.** `require` holds a move into a state until every
  amount due is authorized or captured (`unauthorized`) or captured
  (`unpaid`). No move leaves a state while an attempt is `processing`
  (`payment_in_progress`), so an expiry Timers fires does not let go of
  what is being paid for; Timers records the veto as the deadline's
  outcome. In a `voidOn` state every attempt not captured, failed,
  cancelled or processing gets one `void` action and `openPayment` is
  `nothing_due`; money reported captured there is refunded with
  `afterRelease: refund` and kept for an operator with `keep`. With
  `capture: manual`, a `captureOn` state gives every authorized attempt
  one `capture` action. afterChange reads the status as the instance has
  it, so a Timers fire and a Reactions move are caught.
- **Refunds.** `refundPayment` needs `refunds.permission` and draws on
  one captured attempt; one larger than any attempt has left to refund is
  `exceeds_refundable`, its details naming what is.
- **The refund pair.** A refund instance's Payments has `refundOf: { link }`
  and `issueOn`. Entering an `issueOn` state invokes `refundFor` on the
  instance its link points at, as the principal that moved it, for its
  own amount. That instance takes the call only from an instance whose
  `refundOf` link points here, in an `issueOn` state, for its own amount
  and currency (`not_linked`). One approval pays one refund: while a
  refund the instance asked is pending or paid, `refundFor` returns it
  under its key and refuses any other (`key_reused`), and after one
  failed it takes only the key the refund instance derives from itself
  and the failures before (`<schema>:<id>.<n>`). The adapter reports the
  refund's outcome on the instance refunded (`kind: refunded` or
  `refundFailed`, with `refund`), which invokes `settleRefundFor` on the
  refund instance in the same call, as the adapter: the two schemas'
  `record.permission` must be the same, which defining the refund's
  schema checks, and `settleRefundFor` refuses a status or an amount
  other than what the refunded instance recorded (`not_linked`). The
  engine's policy must let the adapter write the refund schema's
  `settleRefundFor` and the issuing principal write the refunded schema's
  `refundFor`.
- **Delete.** Waits while an attempt is in flight
  (`payment_in_progress`); the recorded events stay, so a redelivery is
  still a repeat.
- **configChange.** `require`, `voidOn`, `captureOn`, `capture`,
  `afterRelease`, `issueOn` and the permissions may change; the currency,
  the source of the amount and `refundOf` stay while the schema has
  instances. Payments joins a schema that has instances and leaves only
  one with none.

### No card data, no raw events

Every params schema is closed: `providerRef` is limited by pattern
(`^[A-Za-z0-9_-]{1,255}$`), the display hint is a `brand` and a
four-digit `last4`, and `metadata` is at most ten string pairs. A
provider's raw event, which holds the payer's name, email and address, is
never stored. A card is entered only in the provider's hosted page or
fields, and a schema must add no card fields of its own.

### Tools

The declarations name no invocation policy, so they register under any
distribution's. A generated build whose engine types compose `Payments`
or `Stock` is refused unless `outputs.engine.tools.invocation` names one
for `Payments.openPayment`, `capturePayment`, `voidPayment`,
`refundPayment` and `refundFor`, and `Stock.receiveStock`, `adjustStock`
and `countStock` (`enginedoc.MoneyOperations`). The engine document
hides Payments' service operations wherever it is composed,
`listPaymentActions`, `recordPaymentEvent` and `settleRefundFor`
(`enginedoc.ServiceOperations`), beside what `tools.hidden` names
(`runtime/engine/README.md`, "Tools").

## Throughput

Every commerce write takes the namespace's writer lock (D58), which is
what keeps two shoppers from holding the last unit, across replicas and
languages. `TestCommerceServersOversellNothing`
(`internal/generator/servergen`) moves 1,000 checkouts to held at once
over a Go, a Rust and a TypeScript server on one Postgres for three
units, and 1,000 bookings of one slot of capacity 1: exactly three and
one hold. `TestCommerceHoldsLoad` measures holds a second at 1, 10 and 50
items a claim when `SUPERSCHEMATIC_COMMERCE_LOAD=1`; D70's Status records
what it measured. The lock makes a namespace one queue of writes, so
each shop is a namespace of its own.

## Runtimes and tests

The npm package ships compiled ESM with declarations (`dist/`), runs on
Node.js 24 and Bun, and needs `@superschematic/engine` next to it in
`node_modules` (a peer dependency; `scripts/link-local-deps.mjs` links
the checkout's).

- **The test kit** (`runtime/engine/rust/testkit`): `commerce_stock`,
  `commerce_holds`, `commerce_calendar`, `commerce_holds_calendar`,
  `commerce_payments`, `commerce_payments_refunds` and `commerce_supply`
  run on SQLite and on Postgres when
  `SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL` names one, checking after
  every step that `Stock.onHand` is the sum of the movements, `held` the
  sum of the records that hold, and that every held or committed item
  matches its supply's record. Two engines over one Postgres race for the
  last units and places.
- **The package's suites** (`typescript/test`): `stock`, `holds`,
  `holds-timers`, `calendar`, `payments`, `payments-refunds` and
  `payments-holds`, on the engine's test addon, under Node.js and Bun.
  `test/corpus.test.ts` records
  `runtime/engine/testdata/scenarios/commerce-*.json`, which the test kit
  replays, and fails when a committed file is not what it records
  (`UPDATE_ENGINE_CORPUS=1` rewrites them).
- **Generated servers** (`internal/generator/servergen`,
  `shop-commerce-db`): `TestCommerceEngineEntrypointServes`, `…Rust` and
  `…TypeScript` build and serve each language's server offline and on
  Postgres; `TestCommerceServersAnswerOneScriptAlike` runs one script
  against the three over one Postgres (stock, a cart's holds,
  `total_unknown`, a payment's events repeated, late and in another
  currency, placing, a refund through Approvals, expiry and the late
  payment through the runner, cross-server reads) and requires their
  transcripts to be identical.

```
make engine-rust          # the workspace, the commerce crate among its members
scripts/engine-napi.sh --js-behaviors
cd runtime/engine-commerce/typescript
bun install --frozen-lockfile
bun run typecheck
bun run test              # build, then test:node and test:bun
make engine-servers       # the generated servers; on Postgres with SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL
```
