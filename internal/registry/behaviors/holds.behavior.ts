// Holds's class (D103): the build derives holds.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { Generic } from "superscalar";
import { computed, declaresBehavior, subTable, type Integer, type JsonSchema, type OneOf } from "@superschematic/db";

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface HoldsConfig {
  /** The holds, by name: camelCase, at most 64 characters. */
  holds: JsonSchema<Record<JsonSchema<string, { pattern: "^[a-z][A-Za-z0-9]{0,63}$" }>, JsonSchema<{
    /**
     * A single-valued link of the type's Links config whose target is the
     * supply: one item.
     */
    link?: JsonSchema<string, { pattern: "^[a-z][A-Za-z0-9]{0,63}$" }>;
    /**
     * An own list field of the type whose elements each name a supply of
     * schema: one item per element.
     */
    each?: {
      /** An own list field of the type, by JSON key: one item per element. */
      field: Named;
      /**
       * The field of each element, by JSON key, that holds the supply's id.
       */
      target: Named;
    };
    /**
     * The supply's schema, for an each hold: it composes a supply, such as
     * Stock or Calendar.
     */
    schema?: Named;
    /**
     * Counted supply: the units each item asks, a count or a field ({ field
     * }), an integer field of the list's element for an each hold, an own
     * field of the type for a link hold.
     */
    quantity?: JsonSchema<Integer, { minimum: 1 }> | {
      /** The field, by JSON key. */
      field: Named;
    };
    /**
     * Timed supply: the time each item asks, from start to end, or to
     * durationMs after start. start and end are date-time fields of the
     * list's element for an each hold, own fields of the type for a link
     * hold.
     */
    interval?: JsonSchema<{
      /** The date-time field the interval starts at, by JSON key. */
      start: Named;
      /** The date-time field it ends at, by JSON key. */
      end?: Named;
      /** How long it lasts, in milliseconds. */
      durationMs?: JsonSchema<Integer, { minimum: 60000 }>;
    }, { oneOf: [{ required: ["end"] }, { required: ["durationMs"] }] }>;
  }, { allOf: [{ oneOf: [{ required: ["link"] }, { required: ["each", "schema"] }] }, { oneOf: [{ required: ["quantity"] }, { required: ["interval"] }] }] }>>, { minProperties: 1; maxProperties: 16 }>;
  /**
   * The states of the type's Workflow whose entry takes the holds, and the
   * create into one of them; the create alone when absent.
   */
  takeOn?: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  /**
   * The states whose entry commits what is held; a move into one while an
   * item is not held is refused.
   */
  commitOn?: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  /**
   * The states whose entry releases what is held, such as a hold's expiry or
   * a cancellation.
   */
  releaseOn?: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  /**
   * The states whose entry returns the units of counted supply the claim
   * committed.
   */
  returnOn?: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  /** The most items the claim's holds may ask together; 100 when absent. */
  maxItems?: JsonSchema<Integer, { minimum: 1; maximum: 1000 }>;
  /**
   * On a table, the Workflow whose status takes, commits, releases and
   * returns the holds: the field a Workflow is anchored on, or Workflow for
   * one the type implements. Implied when the type has one Workflow.
   */
  status?: Named;
}

type RetakeHoldsParams = JsonSchema<{}, { properties: {} }>;

interface RetakeHoldsResult {
  items: {
    /** The hold's name in the config. */
    hold: string;
    /** The item's index in its hold. */
    item: Integer;
    /** The supply's schema. */
    schema: string;
    /** The supply's id. */
    id: string;
    /** The units it asks, of counted supply. */
    quantity?: Integer;
    /** When the time it asks starts, of timed supply. */
    startMs?: Integer;
    /** When it ends. */
    endMs?: Integer;
    state: JsonSchema<"pending" | "held" | "committed" | "released" | "returned", { type: "string" }>;
    /**
     * The unit price the supply held it at, in the currency's minor units.
     */
    unitPrice?: Integer;
    currency?: string;
  }[];
  state: JsonSchema<"none" | "pending" | "held" | "committed" | "released" | "returned", { type: "string" }>;
  /**
   * The sum of the priced items' prices: an item without a price adds
   * nothing, and with none priced it is { amount: 0, currency: null }. Null
   * only when the priced items' currencies differ or the sum passes what a
   * JSON number holds exactly.
   */
  total: OneOf<{
    amount: Integer;
    currency: string | null;
  } | null>;
}

type ListHoldsParams = JsonSchema<{}, { properties: {} }>;

interface ListHoldsResult {
  items: {
    /** The hold's name in the config. */
    hold: string;
    /** The item's index in its hold. */
    item: Integer;
    /** The supply's schema. */
    schema: string;
    /** The supply's id. */
    id: string;
    /** The units it asks, of counted supply. */
    quantity?: Integer;
    /** When the time it asks starts, of timed supply. */
    startMs?: Integer;
    /** When it ends. */
    endMs?: Integer;
    state: JsonSchema<"pending" | "held" | "committed" | "released" | "returned", { type: "string" }>;
    /**
     * The unit price the supply held it at, in the currency's minor units.
     */
    unitPrice?: Integer;
    currency?: string;
    /** For a pending item: the supply's checkHold answer now. */
    check?: {
      /** Whether holdFor would hold the item now. */
      fits: boolean;
      /** When it would not: the code holdFor would veto with. */
      code?: string;
      /** When it would not: why. */
      reason?: string;
      /** When it would not: the veto's details. */
      details?: object;
    };
  }[];
  state: JsonSchema<"none" | "pending" | "held" | "committed" | "released" | "returned", { type: "string" }>;
  /**
   * The sum of the priced items' prices: an item without a price adds
   * nothing, and with none priced it is { amount: 0, currency: null }. Null
   * only when the priced items' currencies differ or the sum passes what a
   * JSON number holds exactly.
   */
  total: OneOf<{
    amount: Integer;
    currency: string | null;
  } | null>;
}

/** The codes Holds's refusals carry. */
enum HoldsVeto {
  /**
   * A create or a move into a takeOn state, or retakeHolds, while an item
   * does not fit its supply (details.items: each item that does not fit, with
   * the supply's code, reason and details).
   */
  unavailable,
  /** A move into a commitOn state while an item is not held. */
  holds_missing,
  /**
   * An update, while the claim holds, of a field a hold reads: the list, an
   * element's target, quantity or interval fields, or the link (details: the
   * fields).
   */
  held_fields,
  /**
   * The delete of a claim that holds committed time that has not ended
   * (details: the items).
   */
  committed,
  /** Holds that ask more items together than the config's maxItems. */
  too_many_items,
  /**
   * retakeHolds while the claim is in no takeOn state, or holds every item.
   */
  not_taking,
  /**
   * An item whose supply does not exist, or whose schema composes no supply.
   */
  no_supply,
}

/**
 * Holds supply for a claim, such as a checkout, a booking or a rental, while
 * its Workflow waits, and lets it go or keeps it as the claim moves. The
 * config names the holds: a link to one supply, or an own list field whose
 * elements each name one, with the units or the time each item asks. When the
 * claim is created into, or moves into, a takeOn state, Holds asks each
 * supply's checkHold, counting the claim's other items of the same supply,
 * and refuses the move with unavailable, listing every item that does not
 * fit; otherwise it holds every item in the same transaction, all or none,
 * through the supply contract's holdFor. Entering a commitOn state commits
 * what is held, a releaseOn state releases it, and a returnOn state returns
 * committed units, whichever operation moved the status: a transition, a
 * Timers deadline, a Lease expiry or a Reactions rule. A hold expires by a
 * Timers deadline that moves the claim into a releaseOn state; Holds has no
 * expiry and no release of its own. While the claim holds, the fields a hold
 * reads do not change. The claim keeps the unit price each supply held an
 * item at, and their total. Deleting the claim releases what it holds; what
 * it committed stays sold or booked, and a claim with committed time to come
 * is not deleted. Requires Workflow.
 */
@declaresBehavior({ requires: ["Workflow"], vetoes: HoldsVeto, asks: { Supply: "Asks each supply an item names whether the item fits (checkHold) before a take, and calls its holdFor, commitFor, releaseFor and returnFor on the supply's row as the claim's status moves." } })
export abstract class Holds<C extends HoldsConfig> {
  /**
   * Each item the claim's holds ask, in hold and item order: { hold, item,
   * schema, id, quantity or startMs and endMs, state, unitPrice?, currency?
   * }, the supply it names, what it asks, its state (pending, held,
   * committed, released, returned), and the unit price the supply held it at.
   * An item's state only advances.
   */
  @computed items: unknown;
  /**
   * The least advanced state of the items: none before any is asked, then
   * pending, held, committed, released or returned. A rule hears it turn
   * committed.
   */
  state: string;
  /**
   * The sum of the priced items' unit prices times their quantities, as {
   * amount, currency }: an item without a price adds nothing, and with none
   * priced it is { amount: 0, currency: null }. Null only when the priced
   * items' currencies differ or the sum passes what a JSON number holds
   * exactly.
   */
  total: Generic.JSON;

  /**
   * Takes the holds again while the claim is in a takeOn state and an item is
   * not held, after a supply refused it or what the items ask was changed:
   * refused with unavailable as a move into the state is, and holds every
   * item or none.
   * @writes
   */
  abstract retakeHolds(params: RetakeHoldsParams): RetakeHoldsResult;

  /**
   * Lists the claim's items, as Holds.items has them, with each supply's
   * answer to checkHold for an item still pending, counting the claim's other
   * pending items of the same supply.
   */
  abstract listHolds(params: ListHoldsParams): ListHoldsResult;

  /**
   * On a table, each item the claim has taken, keyed by its hold and index:
   * the supply it names, what it asks, its state and the unit price the
   * supply held it at, and the Workflow entry it was taken at. An item its
   * fields ask that it has not taken is no row; the items field shows it
   * pending.
   */
  @subTable({ key: "key" })
  static items = class {
    /** The hold's name and the item's index, joined by /. */
    key: string;
    /** The hold's name. */
    hold: string;
    /** The item's index in its hold. */
    item: Generic.Int64;
    /** The supply's type. */
    supplySchema: string;
    /** The supply's key. */
    supplyId: string;
    /** The units of counted supply it asks. */
    quantity: Generic.Int64;
    /** When the time it asks starts, in epoch milliseconds. */
    startMs: Generic.Int64;
    /** When the time it asks ends, in epoch milliseconds. */
    endMs: Generic.Int64;
    /** pending, held, committed, released or returned. */
    state: string;
    /**
     * The unit price the supply held it at, in the currency's minor units.
     */
    unitPrice: Generic.Int64;
    /** The unit price's currency. */
    currency: string;
    /**
     * The Workflow's enteredAt at the take, which tells a later entry of a
     * takeOn state from this one.
     */
    takenAt: Generic.Int64;
  };
}
