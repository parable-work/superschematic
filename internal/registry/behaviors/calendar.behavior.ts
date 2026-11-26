// Calendar's class (D103): the build derives calendar.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { Generic, Temporal } from "superscalar";
import { declaresBehavior, subTable, volatile, type Integer, type JsonSchema, type OneOf, type Supply } from "@superschematic/db";

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface CalendarConfig {
  /**
   * The IANA time zone the hours are wall times in, such as Europe/London; an
   * instance may set its own.
   */
  timeZone: JsonSchema<string, { minLength: 1; maxLength: 64; pattern: "^[A-Za-z][A-Za-z0-9_+/-]*$" }>;
  /**
   * The weekly opening hours an instance has until it sets its own: each day,
   * mon to sun, a list of [open, close] wall-time ranges, each closing after
   * it opens, none overlapping. A day absent or empty is closed.
   */
  hours: {
    mon?: JsonSchema<([JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>, JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>])[], { maxItems: 12 }>;
    tue?: JsonSchema<([JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>, JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>])[], { maxItems: 12 }>;
    wed?: JsonSchema<([JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>, JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>])[], { maxItems: 12 }>;
    thu?: JsonSchema<([JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>, JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>])[], { maxItems: 12 }>;
    fri?: JsonSchema<([JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>, JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>])[], { maxItems: 12 }>;
    sat?: JsonSchema<([JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>, JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>])[], { maxItems: 12 }>;
    sun?: JsonSchema<([JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>, JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>])[], { maxItems: 12 }>;
  };
  /**
   * How many holds may overlap at any instant: a count, or an own integer
   * field of the type ({ field }) whose value of at least 1 is the
   * instance's; 1 when absent.
   */
  capacity?: JsonSchema<Integer, { minimum: 1; maximum: 10000 }> | {
    /** An own integer field of the type, by JSON key. */
    field: Named;
  };
  /**
   * The start grid, in milliseconds: a hold starts a whole number of slots
   * after an opening time. Any whole minute when absent.
   */
  slotMs?: JsonSchema<Integer, { minimum: 60000; maximum: 86400000 }>;
  /** The shortest hold, in milliseconds; slotMs when absent. */
  minMs?: JsonSchema<Integer, { minimum: 60000 }>;
  /** The longest hold, in milliseconds; a day when absent. */
  maxMs?: JsonSchema<Integer, { minimum: 60000; maximum: 2678400000 }>;
  /**
   * Time kept free around each hold, which counts against capacity as the
   * hold does.
   */
  bufferMs?: {
    /**
     * Milliseconds before a hold that no other may overlap; 0 when absent.
     */
    before?: JsonSchema<Integer, { minimum: 0; maximum: 86400000 }>;
    /** Milliseconds after a hold that no other may overlap; 0 when absent. */
    after?: JsonSchema<Integer, { minimum: 0; maximum: 86400000 }>;
  };
  /**
   * How long ahead of its start a hold must be made, by the engine's clock; 0
   * when absent.
   */
  leadMs?: JsonSchema<Integer, { minimum: 0 }>;
  /**
   * How far ahead a hold may start, by the engine's clock; no limit when
   * absent.
   */
  horizonMs?: JsonSchema<Integer, { minimum: 1 }>;
  /**
   * The permission a caller needs to set an instance's hours and to add or
   * remove its closures; a closure over held time needs its .override. Nobody
   * may when absent.
   */
  permission?: Named;
}

interface SetOpeningHoursParams {
  /** The instance's hours, or null for the config's. */
  hours?: OneOf<JsonSchema<{
    mon?: JsonSchema<([JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>, JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>])[], { maxItems: 12 }>;
    tue?: JsonSchema<([JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>, JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>])[], { maxItems: 12 }>;
    wed?: JsonSchema<([JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>, JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>])[], { maxItems: 12 }>;
    thu?: JsonSchema<([JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>, JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>])[], { maxItems: 12 }>;
    fri?: JsonSchema<([JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>, JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>])[], { maxItems: 12 }>;
    sat?: JsonSchema<([JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>, JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>])[], { maxItems: 12 }>;
    sun?: JsonSchema<([JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>, JsonSchema<string, { description: "A wall time, HH:MM, from 00:00 to 24:00."; pattern: "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$" }>])[], { maxItems: 12 }>;
  }, { description: "Opening hours by day of the week, mon to sun: each day a list of [open, close] wall-time ranges in the time zone, each closing after it opens, none overlapping. A day absent or empty is closed." }> | null>;
  /** The instance's time zone, or null for the config's. */
  timeZone?: OneOf<JsonSchema<JsonSchema<string, { minLength: 1; maxLength: 64; pattern: "^[A-Za-z][A-Za-z0-9_+/-]*$" }>, { description: "An IANA time zone, such as Europe/London." }> | null>;
}

interface SetOpeningHoursResult {
  /** The instance's hours after the call. */
  hours: object;
  /** The instance's time zone after the call. */
  timeZone: string;
}

interface AddClosureParams {
  /** When it starts, in epoch milliseconds. */
  startMs: Integer;
  /** When it ends, after startMs, in epoch milliseconds. */
  endMs: Integer;
  /** Why, for the record. */
  reason: JsonSchema<string, { minLength: 1; maxLength: 500 }>;
  /** Close over held time. */
  force?: boolean;
}

interface AddClosureResult {
  /** The closure's id. */
  closure: string;
  /** When it starts, in epoch milliseconds. */
  startMs: Integer;
  /** When it ends, in epoch milliseconds. */
  endMs: Integer;
  reason: string;
}

interface RemoveClosureParams {
  /** The closure's id. */
  closure: Named;
}

interface RemoveClosureResult {
  /** The closure's id. */
  closure: string;
  /** When it starts, in epoch milliseconds. */
  startMs: Integer;
  /** When it ends, in epoch milliseconds. */
  endMs: Integer;
  reason: string;
}

interface HoldForParams {
  /** The claim the call is for: its schema and id. */
  holder: {
    /** The claim's schema. */
    schema: Named;
    /** The claim's id. */
    id: Named;
  };
  /** The hold's name in the claim's Holds config. */
  hold: Named;
  /**
   * The item's index in its hold: its element's in an each hold's list, 0 for
   * a link hold.
   */
  item: JsonSchema<Integer, { minimum: 0; maximum: 9007199254740991 }>;
  /** The time the item asks, from startMs to endMs, in epoch milliseconds. */
  interval: {
    /** When it starts, no earlier than 1900-01-01T00:00Z. */
    startMs: JsonSchema<Integer, { minimum: -2208988800000; maximum: 221845392000000 }>;
    /** When it ends, after startMs and no later than 9000-01-01T00:00Z. */
    endMs: JsonSchema<Integer, { minimum: -2208988800000; maximum: 221845392000000 }>;
  };
}

interface HoldForResult {
  /** The item's state in this supply's record after the call. */
  state: JsonSchema<"held" | "committed" | "released" | "returned", { type: "string" }>;
  /** Whether the call moved the record; false for a call made again. */
  changed: boolean;
  /**
   * The unit price the item was held at, in the currency's minor units, when
   * the supply names one.
   */
  unitPrice?: Integer;
  /** The unit price's currency. */
  currency?: string;
}

interface CommitForParams {
  /** The claim the call is for: its schema and id. */
  holder: {
    /** The claim's schema. */
    schema: Named;
    /** The claim's id. */
    id: Named;
  };
  /** The hold's name in the claim's Holds config. */
  hold: Named;
  /**
   * The item's index in its hold: its element's in an each hold's list, 0 for
   * a link hold.
   */
  item: JsonSchema<Integer, { minimum: 0; maximum: 9007199254740991 }>;
  /** The time the item asks, from startMs to endMs, in epoch milliseconds. */
  interval: {
    /** When it starts, no earlier than 1900-01-01T00:00Z. */
    startMs: JsonSchema<Integer, { minimum: -2208988800000; maximum: 221845392000000 }>;
    /** When it ends, after startMs and no later than 9000-01-01T00:00Z. */
    endMs: JsonSchema<Integer, { minimum: -2208988800000; maximum: 221845392000000 }>;
  };
}

interface CommitForResult {
  /** The item's state in this supply's record after the call. */
  state: JsonSchema<"held" | "committed" | "released" | "returned", { type: "string" }>;
  /** Whether the call moved the record; false for a call made again. */
  changed: boolean;
  /**
   * The unit price the item was held at, in the currency's minor units, when
   * the supply names one.
   */
  unitPrice?: Integer;
  /** The unit price's currency. */
  currency?: string;
}

interface ReleaseForParams {
  /** The claim the call is for: its schema and id. */
  holder: {
    /** The claim's schema. */
    schema: Named;
    /** The claim's id. */
    id: Named;
  };
  /** The hold's name in the claim's Holds config. */
  hold: Named;
  /**
   * The item's index in its hold: its element's in an each hold's list, 0 for
   * a link hold.
   */
  item: JsonSchema<Integer, { minimum: 0; maximum: 9007199254740991 }>;
  /** The time the item asks, from startMs to endMs, in epoch milliseconds. */
  interval: {
    /** When it starts, no earlier than 1900-01-01T00:00Z. */
    startMs: JsonSchema<Integer, { minimum: -2208988800000; maximum: 221845392000000 }>;
    /** When it ends, after startMs and no later than 9000-01-01T00:00Z. */
    endMs: JsonSchema<Integer, { minimum: -2208988800000; maximum: 221845392000000 }>;
  };
}

interface ReleaseForResult {
  /** The item's state in this supply's record after the call. */
  state: JsonSchema<"held" | "committed" | "released" | "returned", { type: "string" }>;
  /** Whether the call moved the record; false for a call made again. */
  changed: boolean;
  /**
   * The unit price the item was held at, in the currency's minor units, when
   * the supply names one.
   */
  unitPrice?: Integer;
  /** The unit price's currency. */
  currency?: string;
}

interface CheckHoldParams {
  /** The claim the call is for: its schema and id. */
  holder: {
    /** The claim's schema. */
    schema: Named;
    /** The claim's id. */
    id: Named;
  };
  /** The hold's name in the claim's Holds config. */
  hold: Named;
  /**
   * The item's index in its hold: its element's in an each hold's list, 0 for
   * a link hold.
   */
  item: JsonSchema<Integer, { minimum: 0; maximum: 9007199254740991 }>;
  /** The time the item asks, from startMs to endMs, in epoch milliseconds. */
  interval: {
    /** When it starts, no earlier than 1900-01-01T00:00Z. */
    startMs: JsonSchema<Integer, { minimum: -2208988800000; maximum: 221845392000000 }>;
    /** When it ends, after startMs and no later than 9000-01-01T00:00Z. */
    endMs: JsonSchema<Integer, { minimum: -2208988800000; maximum: 221845392000000 }>;
  };
  /**
   * What the claim's other items still to hold ask of this instance, which
   * the answer counts as taken.
   */
  also?: JsonSchema<{
    /**
     * The time the item asks, from startMs to endMs, in epoch milliseconds.
     */
    interval: {
      /** When it starts, no earlier than 1900-01-01T00:00Z. */
      startMs: JsonSchema<Integer, { minimum: -2208988800000; maximum: 221845392000000 }>;
      /** When it ends, after startMs and no later than 9000-01-01T00:00Z. */
      endMs: JsonSchema<Integer, { minimum: -2208988800000; maximum: 221845392000000 }>;
    };
  }[], { maxItems: 100 }>;
}

interface CheckHoldResult {
  /** Whether holdFor would hold the item now. */
  fits: boolean;
  /** When it would not: the code holdFor would veto with. */
  code?: string;
  /** When it would not: why. */
  reason?: string;
  /** When it would not: the veto's details. */
  details?: object;
}

interface ListBookingsParams {
  /** The window's start, in epoch milliseconds. */
  startMs: Integer;
  /** The window's end, after startMs and at most 31 days after it. */
  endMs: Integer;
  /** At most this many; 50 when absent. */
  limit?: JsonSchema<Integer, { minimum: 1; maximum: 500 }>;
  /** The next of the previous page. */
  cursor?: string;
}

interface ListBookingsResult {
  items: {
    /** The claim. */
    holder: {
      schema: Named;
      id: Named;
    };
    /** The claim's hold. */
    hold: string;
    /** The item's index in its hold. */
    item: Integer;
    startMs: Integer;
    endMs: Integer;
    state: JsonSchema<"held" | "committed", { type: "string" }>;
    /**
     * True for a deleted claim's committed time, which stays booked; absent
     * otherwise.
     */
    retired?: boolean;
  }[];
  /** The cursor of the next page; null after the last. */
  next: string | null;
}

interface FindAvailabilityParams {
  /** The window's start, in epoch milliseconds. */
  startMs: Integer;
  /** The window's end, after startMs and at most 31 days after it. */
  endMs: Integer;
  /** How long each slot is, in milliseconds. */
  durationMs: JsonSchema<Integer, { minimum: 60000 }>;
  /** Only these instances. */
  ids?: JsonSchema<JsonSchema<string, { pattern: "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$" }>[], { minItems: 1; maxItems: 100; uniqueItems: true }>;
  /** Only the instances a list with this filter returns. */
  where?: object;
  /** At most this many slots; 100 when absent. */
  limit?: JsonSchema<Integer, { minimum: 1; maximum: 500 }>;
}

interface FindAvailabilityResult {
  slots: {
    /** The instance. */
    id: string;
    startMs: Integer;
    endMs: Integer;
    /** How many more holds the instance can take over the slot. */
    remaining: Integer;
  }[];
  /**
   * Whether more slots fit than limit returned, more instances matched than
   * were read, or the search stopped at its bound of work.
   */
  truncated: boolean;
}

/** The codes Calendar's refusals carry. */
enum CalendarVeto {
  /**
   * The claim the call names does not ask it: it has no such item for this
   * supply, the item asks otherwise, or it is not in the state the call
   * needs; or this supply's record of the item does not move that way.
   */
  not_requested,
  /**
   * What the item asks does not fit what this supply has free (details:
   * supply, hold, item, asked, and available or conflict).
   */
  unavailable,
  /**
   * A supply is not deleted while it holds anything for a claim: an item
   * held, or committed time that has not ended.
   */
  held,
  /**
   * holdFor of an interval not inside the instance's opening hours (details
   * as unavailable's).
   */
  outside_hours,
  /**
   * holdFor of an interval that overlaps a closure (details as unavailable's,
   * with the closure).
   */
  closed,
  /**
   * holdFor of an interval that does not start on the config's slotMs grid.
   */
  off_grid,
  /** holdFor of an interval shorter than the config's minMs. */
  too_short,
  /** holdFor of an interval longer than the config's maxMs. */
  too_long,
  /**
   * holdFor of an interval that starts sooner than the config's leadMs from
   * now.
   */
  too_soon,
  /**
   * holdFor of an interval that starts further ahead than the config's
   * horizonMs.
   */
  too_far,
  /**
   * addClosure over time a claim holds, without force, or setOpeningHours of
   * another time zone while the instance holds time that has not ended
   * (details: the holds).
   */
  has_bookings,
  /** listBookings or findAvailability over a window longer than 31 days. */
  window_too_long,
  /** removeClosure of a closure the instance does not have. */
  no_closure,
}

/**
 * Opens time on a resource a claim books: a stylist, a chair, a room, a
 * collection counter, a pool of bikes. Weekly opening hours in an IANA time
 * zone, which an instance may set for itself, less its closures, give the
 * times a hold may take; capacity says how many holds may overlap. A claim
 * takes time only through the supply contract's operations, which Holds
 * invokes by name: holdFor holds an interval an item of the claim asks,
 * commitFor books it for good and releaseFor frees it. Each checks the
 * claim's own Holds.items, so no caller holds or frees time by calling one
 * directly, and each is idempotent. holdFor refuses an interval outside the
 * hours or in a closure, off the start grid, too short or too long, too soon
 * or too far ahead by the engine's clock, or beyond capacity at any instant,
 * buffers included. findAvailability answers free slots across the schema's
 * instances by the same rules. Wall times become instants through a time zone
 * database the engine carries, so every server built from one lock file
 * computes the same slots; a wall time a daylight-saving change skips has no
 * slot. An instance is not deleted while it holds time for a claim: time
 * held, or committed and not yet ended. Calendar and Stock declare the same
 * contract operations, so a type composes one of them.
 */
@declaresBehavior({ conflicts: ["Stock"], vetoes: CalendarVeto })
export abstract class Calendar<C extends CalendarConfig> implements /** Time a claim holds: holdFor, commitFor and releaseFor, which Holds calls on the row, and checkHold, which it asks of the row before a take. */ Supply {
  /**
   * The time zone the instance's hours are in: its own when setOpeningHours
   * set one, else the config's.
   */
  @volatile timeZone: unknown;
  /**
   * The instance's weekly opening hours: its own when setOpeningHours set
   * them, else the config's.
   */
  @volatile hours: unknown;
  /**
   * The closures that have not ended, soonest first, at most 100: { closure,
   * startMs, endMs, reason }.
   */
  @volatile closures: unknown;

  /**
   * Sets the instance's own weekly hours, its time zone, or both; null for
   * either goes back to the config's. Holds already made stay; the time zone
   * changes only while the instance holds no time that has not ended. Needs
   * the config's permission.
   * @writes
   */
  abstract setOpeningHours(params: SetOpeningHoursParams): SetOpeningHoursResult;

  /**
   * Closes the instance from startMs to endMs, with a reason. Needs the
   * config's permission; refused over time a claim holds unless force is set,
   * which needs the permission's .override. Forcing keeps the holds: the
   * claims are moved by their own Workflow, by a rule or by hand.
   * @writes
   */
  abstract addClosure(params: AddClosureParams): AddClosureResult;

  /**
   * Removes a closure. Needs the config's permission.
   * @writes
   */
  abstract removeClosure(params: RemoveClosureParams): RemoveClosureResult;

  /**
   * Holds what an item of a claim's Holds asks, for that claim, once the
   * claim's item is held. Refused when it does not fit, and for an item the
   * claim does not hold.
   * @writes
   */
  abstract holdFor(params: HoldForParams): HoldForResult;

  /**
   * Makes what an item of a claim holds the claim's for good, once the
   * claim's item is committed.
   * @writes
   */
  abstract commitFor(params: CommitForParams): CommitForResult;

  /**
   * Frees what an item of a claim holds, once the claim's item is released.
   * @writes
   */
  abstract releaseFor(params: ReleaseForParams): ReleaseForResult;

  /**
   * Answers whether holdFor would hold what an item asks now, and when not,
   * why.
   */
  abstract checkHold(params: CheckHoldParams): CheckHoldResult;

  /**
   * Lists the holds the instance has over a window of at most 31 days, by
   * start, one page at a time: each claim's item, its interval and its state,
   * held or committed, and retired for a deleted claim's committed time.
   */
  abstract listBookings(params: ListBookingsParams): ListBookingsResult;

  /**
   * Finds free slots of a duration across the schema's instances over a
   * window of at most 31 days, by holdFor's rules at the engine's clock: at
   * most limit slots, soonest first, each with the instance and how many more
   * holds it can take then. ids or where narrow the instances, as a list
   * does.
   * @scope schema
   */
  abstract findAvailability(params: FindAvailabilityParams): FindAvailabilityResult;

  /**
   * On a table, the supply's record of each claim item that holds or held
   * time of the row, keyed by the claim's type and key, the hold and the
   * item's index: the interval it asked, its state, which a call moves only
   * forward, so a call made again changes nothing, and when it last moved. A
   * deleted claim's records retire under a key of their own, its committed
   * time still booked, so a claim created again with its key holds afresh.
   */
  @subTable({ key: "record" })
  static bookings = class {
    /**
     * The claim's type, its key, the hold and the item's index, joined by /;
     * a retired record's has # and a key of its own after it.
     */
    record: string;
    /** The claim's type. */
    holderSchema: string;
    /** The claim's key. */
    holderId: string;
    /** The hold's name in the claim's Holds config. */
    hold: string;
    /** The item's index in its hold. */
    item: Generic.Int64;
    /** When the time it asked starts, in epoch milliseconds. */
    startMs: Generic.Int64;
    /** When the time it asked ends, in epoch milliseconds. */
    endMs: Generic.Int64;
    /** held, committed or released. */
    state: string;
    /** A price the supply held it at; Calendar gives none. */
    unitPrice: Generic.Int64;
    /** The price's currency; Calendar gives none. */
    currency: string;
    /** When it last moved, in epoch milliseconds. */
    at: Temporal.DateTime;
    /** Whether it is a deleted claim's. */
    retired: boolean;
  };

  /**
   * On a table, the row's closures, keyed by their name, closure-1 onward:
   * when each starts and ends, why, who made it and when.
   */
  @subTable({ key: "closure" })
  static closures = class {
    /** The closure's name. */
    closure: string;
    /** When it starts, in epoch milliseconds. */
    startMs: Generic.Int64;
    /** When it ends, in epoch milliseconds. */
    endMs: Generic.Int64;
    /** Why, as the caller gave it. */
    reason: string;
    /** The subject of the caller who made it. */
    by: string;
    /** When it was made, in epoch milliseconds. */
    at: Temporal.DateTime;
  };

  /**
   * On a table, what setOpeningHours and addClosure set of the row, keyed by
   * name: hours, the row's own weekly hours; timeZone, its own zone;
   * closuresMade, how many closures it has made, which names the next. A name
   * with no row follows the config.
   */
  @subTable({ key: "name" })
  static settings = class {
    /** hours, timeZone or closuresMade. */
    name: string;
    /** Its value: the hours by day, the zone's IANA name, or the count. */
    value: Generic.JSON;
  };
}
