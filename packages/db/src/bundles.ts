// Bundles (D103, step 5): a bundle presets the wiring of existing
// behaviors and exposes only the settings that vary. It defines no rules
// of its own. A table implements one as it implements a behavior,
// `export abstract class Booking implements Reservation<Booking, { ... }>`,
// and the reader substitutes the table's settings into the parts' configs
// through the checker: each part becomes a use of the table, named as the
// use it is (holdsState, status, transitionStatus), so the build, the core
// and every generator meet the parts alone. The data forms name a bundle
// and its settings, `{ "name": "Reservation", "settings": { ... } }`, and
// the loader expands them from the registry's declaration
// (internal/registry/bundles), which these aliases are held to.
import type { Holds, Lease, Payments, Rollups, Seats, StateRules, Timers, Workflow } from "./wrappers";

/**
 * A bundle's type (D103, step 5): `type Name<T, S extends { ... }> =
 * Bundle<Parts, Fields>`. Parts is a tuple of the behaviors the table
 * implements, each its carrier with the bundle's table T and a config
 * (`Holds<T, { ... }>`); Fields an object type of the behaviors it anchors
 * on fields, by field (`{ status: Workflow<{ ... }> }`). A config reads a
 * setting as `S["name"]`, written in place, and a member whose setting the
 * table leaves out is left out. A project may declare its own bundle in a
 * schema file, which TypeScript alone reads. Like a behavior's carrier, it
 * resolves to the empty object type, so a table that implements a bundle
 * declares none of its fields.
 */
export type Bundle<Parts extends readonly unknown[], Fields extends object = {}> = NonNullable<unknown>;

/** Reservation's settings (D103): the booking's resource, interval, amount and currency. */
export interface ReservationSettings<T> {
  /** The to-one relation of the table to the resource whose time a booking holds: a table that implements Calendar. */
  readonly resource: Extract<keyof T, string>;
  /** The own date-time fields the booking starts and ends at. */
  readonly interval: { readonly start: Extract<keyof T, string>; readonly end: Extract<keyof T, string> };
  /** The own integer field that holds what a booking owes, in the currency's minor units. */
  readonly amount: Extract<keyof T, string>;
  /** The currency of the amount: an ISO 4217 code. */
  readonly currency: string;
}

/**
 * A booking of a resource's time that waits for its payment (D103):
 * `export abstract class Booking implements Reservation<Booking, {
 * resource: "desk"; interval: { start: "slotStart"; end: "slotEnd" };
 * amount: "priceCents"; currency: "GBP" }>`. Its parts: a Workflow on
 * status (draft, held, booked, expired, cancelled); Holds, which takes the
 * resource's time as the booking enters held, commits it as it is booked
 * and lets it go as it expires or is cancelled; Payments, which owes the
 * amount, holds booked until it is paid and voids what is authorized when
 * the booking expires or is cancelled; and a Timers deadline that expires
 * a booking fifteen minutes after it enters held. The resource implements
 * Calendar, whose hours and capacity are its own.
 */
export type Reservation<T, S extends ReservationSettings<T>> = Bundle<
  [
    Holds<
      T,
      {
        holds: { slot: { link: S["resource"]; interval: S["interval"] } };
        takeOn: ["held"];
        commitOn: ["booked"];
        releaseOn: ["expired", "cancelled"];
      }
    >,
    Payments<
      T,
      {
        amount: { field: S["amount"] };
        currency: S["currency"];
        require: { booked: "paid" };
        voidOn: ["expired", "cancelled"];
        record: { permission: "payments.record" };
      }
    >,
    Timers<T, { deadlines: { holdExpiry: { start: { enters: "held" }; afterMs: 900000; then: { transition: "expired" } } } }>,
  ],
  {
    status: Workflow<{
      states: ["draft", "held", "booked", "expired", "cancelled"];
      initial: "draft";
      transitions: [
        { from: "draft"; to: "held" },
        { from: "held"; to: "booked" },
        { from: "held"; to: "expired" },
        { from: "held"; to: "cancelled" },
        { from: "draft"; to: "cancelled" },
      ];
    }>;
  }
>;

/** Barrier's settings (D104): the rows it waits for, through which relation, how long, and who releases it. */
export interface BarrierSettings {
  /** The table whose rows relate to the barrier, each ready once its Workflow finishes with the outcome success. */
  readonly rows: string;
  /** The to-one relation of that table to the barrier. */
  readonly link: string;
  /** How long after the barrier begins waiting its deadline releases it, in milliseconds. */
  readonly timeoutMs: number;
  /** The permission a caller needs to release the barrier: a person's go-ahead. */
  readonly releasePermission: string;
}

/**
 * A point an update waits at (D104): `export abstract class Gate
 * implements Barrier<Gate, { rows: "Agent"; link: "gate"; timeoutMs:
 * 300000; releasePermission: "gates.release" }>`. Its parts: Rollups' all
 * over the related rows, ready when each has finished with the outcome
 * success, which gates released; a Timers deadline whose then releases the
 * barrier once the timeout has passed; and a Workflow on status (waiting,
 * released, cancelled) whose release needs the permission, a person's
 * go-ahead. The gate holds the deadline's and the go-ahead's moves alike:
 * a barrier is released once every row is ready, the deadline vetoed
 * until then coming back every minute.
 */
export type Barrier<T, S extends BarrierSettings> = Bundle<
  [
    Rollups<
      T,
      {
        rollups: {
          ready: { schema: S["rows"]; link: S["link"]; function: "all"; outcomes: ["success"]; gatedStates: ["released"] };
        };
      }
    >,
    Timers<T, { deadlines: { timeout: { start: { enters: "waiting" }; afterMs: S["timeoutMs"]; then: { transition: "released" } } } }>,
  ],
  {
    status: Workflow<{
      states: ["waiting", "released", "cancelled"];
      initial: "waiting";
      transitions: [
        { from: "waiting"; to: "released"; permission: S["releasePermission"] },
        { from: "waiting"; to: "cancelled" },
      ];
      outcomes: { cancelled: "neutral" };
    }>;
  }
>;

/** Mailbox's settings (D104): how long a taker's lease lasts, and who may take a message. */
export interface MailboxSettings {
  /** How long a taker's lease lasts after its acquire or its last heartbeat, in milliseconds; Lease's 60000 when absent. */
  readonly ttlMs?: number;
  /** The permission a caller needs to take a message, Lease's acquirePermission; any caller who may write the row when absent. */
  readonly takePermission?: string;
}

/**
 * A message an agent takes in turn (D104): `export abstract class Message
 * implements Mailbox<Message, { ttlMs: 30000 }>`, beside its own
 * createdBy, which holds the sender's subject. Its parts: StateRules
 * freezing every own field of a taken message; Lease, whose expiry, abandon
 * or release moves a taken message back to queued; and a Workflow on status
 * (queued, urgent, taken, sent, canceled, failed) whose sender's moves
 * (urgent, back to queued, canceled) the subject createdBy holds makes. A
 * take is the app's: a declared read of a conversation's oldest waiting
 * message, Lease's acquire, and the move to taken with the token.
 */
export type Mailbox<T, S extends MailboxSettings> = Bundle<
  [
    StateRules<T, { states: { taken: { frozen: "all" } } }>,
    Lease<T, { onExpiry: { transition: "queued"; from: ["taken"] }; ttlMs: S["ttlMs"]; acquirePermission: S["takePermission"] }>,
  ],
  {
    status: Workflow<{
      states: ["queued", "urgent", "taken", "sent", "canceled", "failed"];
      initial: "queued";
      transitions: [
        { from: "queued"; to: "urgent"; by: ["createdBy"] },
        { from: "urgent"; to: "queued"; by: ["createdBy"] },
        { from: "queued"; to: "canceled"; by: ["createdBy"] },
        { from: "urgent"; to: "canceled"; by: ["createdBy"] },
        { from: "queued"; to: "taken" },
        { from: "urgent"; to: "taken" },
        { from: "taken"; to: "sent" },
        { from: "taken"; to: "failed" },
        { from: "taken"; to: "queued" },
      ];
      outcomes: { canceled: "neutral"; failed: "failure" };
    }>;
  }
>;

/** Assignment's settings (D104): who may take the row for itself, and who may assign another. */
export interface AssignmentSettings {
  /** The permission a caller needs to assign the row to itself; any caller may when absent. */
  readonly take?: string;
  /** The permission a caller needs to assign the row to another subject or unassign another; no caller may when absent. */
  readonly assign?: string;
}

/**
 * Whom a row is assigned to (D104): `export abstract class Problem
 * implements Assignment<Problem, { assign: "problems.assign" }>`. Its part:
 * Seats with one seat, assignee, of one subject at most, which refuses a
 * take while it is full. A Workflow transition's or a StateRules state's by
 * names the seat as "seat:assignee". D104's guard on another principal's
 * Lease acquire, which reads the seat, waits for the declarative
 * vocabulary's caller operand.
 */
export type Assignment<T, S extends AssignmentSettings> = Bundle<
  [Seats<T, { seats: { assignee: { max: 1; onFull: "refuse"; take: S["take"]; assign: S["assign"] } } }>]
>;
