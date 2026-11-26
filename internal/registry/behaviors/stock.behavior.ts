// Stock's class (D103): the build derives stock.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { Generic, Temporal } from "superscalar";
import { computed, declaresBehavior, subTable, volatile, type Integer, type JsonSchema, type Supply } from "@superschematic/db";

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface StockConfig {
  /**
   * The unit price holdFor returns and Holds keeps on each item, so a line
   * keeps the price it was held at: an own integer field of the type and its
   * currency. No price when absent.
   */
  price?: {
    /**
     * An own integer field of the type, by JSON key, holding the unit price
     * in the currency's minor units, such as a Finance.Money field.
     */
    field: Named;
    /**
     * The price's currency: an ISO 4217 code, or an own Finance.Currency
     * field of the type ({ field }) that holds one.
     */
    currency: JsonSchema<JsonSchema<string, { pattern: "^[A-Z]{3}$" }>, { description: "An ISO 4217 code; the engine checks it is one." }> | {
      /** An own Finance.Currency field of the type, by JSON key. */
      field: Named;
    };
  };
  /**
   * Stock.low is true while available is at or below this: a count, or an own
   * integer field of the type ({ field }) that holds one, while it holds one.
   * Stock.low is false when absent.
   */
  lowAt?: JsonSchema<Integer, { minimum: 0 }> | {
    /** An own integer field of the type, by JSON key. */
    field: Named;
  };
  /** The most units one item of a claim may hold; no limit when absent. */
  maxPerHold?: JsonSchema<Integer, { minimum: 1 }>;
  /**
   * Whether a hold may take more than is available, leaving available below
   * 0; false when absent.
   */
  backorder?: boolean;
  /**
   * The permission a caller needs to receive, adjust and count stock; a count
   * below what claims hold needs its .override.
   */
  permission: Named;
}

interface ReceiveStockParams {
  /** How many units arrived. */
  quantity: JsonSchema<Integer, { minimum: 1; maximum: 9007199254740991 }>;
  /** Why, for the record. */
  reason: JsonSchema<string, { minLength: 1; maxLength: 500 }>;
  /** What the units came with, such as a delivery note's number. */
  reference?: JsonSchema<string, { minLength: 1; maxLength: 255 }>;
}

interface ReceiveStockResult {
  /** Stock.onHand after the call. */
  onHand: Integer;
  /** Stock.held after the call. */
  held: Integer;
  /** Stock.available after the call. */
  available: Integer;
  /**
   * The sequence number of the movement the call recorded, per instance; null
   * when it changed nothing.
   */
  movement: Integer | null;
}

interface AdjustStockParams {
  /** The units to add; negative to take away. 0 changes nothing. */
  delta: JsonSchema<Integer, { minimum: -9007199254740991; maximum: 9007199254740991 }>;
  /** Why, for the record. */
  reason: JsonSchema<string, { minLength: 1; maxLength: 500 }>;
}

interface AdjustStockResult {
  /** Stock.onHand after the call. */
  onHand: Integer;
  /** Stock.held after the call. */
  held: Integer;
  /** Stock.available after the call. */
  available: Integer;
  /**
   * The sequence number of the movement the call recorded, per instance; null
   * when it changed nothing.
   */
  movement: Integer | null;
}

interface CountStockParams {
  /** The units counted. */
  onHand: JsonSchema<Integer, { minimum: 0; maximum: 9007199254740991 }>;
  /** Why, for the record. */
  reason: JsonSchema<string, { minLength: 1; maxLength: 500 }>;
}

interface CountStockResult {
  /** Stock.onHand after the call. */
  onHand: Integer;
  /** Stock.held after the call. */
  held: Integer;
  /** Stock.available after the call. */
  available: Integer;
  /**
   * The sequence number of the movement the call recorded, per instance; null
   * when it changed nothing.
   */
  movement: Integer | null;
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
  /** How many units the item asks. */
  quantity: JsonSchema<Integer, { minimum: 1; maximum: 9007199254740991 }>;
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
  /** How many units the item asks. */
  quantity: JsonSchema<Integer, { minimum: 1; maximum: 9007199254740991 }>;
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
  /** How many units the item asks. */
  quantity: JsonSchema<Integer, { minimum: 1; maximum: 9007199254740991 }>;
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

interface ReturnForParams {
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
  /** How many units the item asks. */
  quantity: JsonSchema<Integer, { minimum: 1; maximum: 9007199254740991 }>;
}

interface ReturnForResult {
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
  /** How many units the item asks. */
  quantity: JsonSchema<Integer, { minimum: 1; maximum: 9007199254740991 }>;
  /**
   * What the claim's other items still to hold ask of this instance, which
   * the answer counts as taken.
   */
  also?: JsonSchema<{
    /** How many units the item asks. */
    quantity: JsonSchema<Integer, { minimum: 1; maximum: 9007199254740991 }>;
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

interface ListStockMovementsParams {
  /** At most this many; 50 when absent. */
  limit?: JsonSchema<Integer, { minimum: 1; maximum: 500 }>;
  /** The next of the previous page. */
  cursor?: string;
}

interface ListStockMovementsResult {
  items: {
    /** The movement's sequence number, per instance, from 1. */
    seq: Integer;
    /**
     * What moved the count: receive, adjust and count by a caller with the
     * permission, commit by a claim's commitFor, return by its returnFor.
     */
    kind: JsonSchema<"receive" | "adjust" | "count" | "commit" | "return", { type: "string" }>;
    /** The units it added; negative for units taken away. */
    delta: Integer;
    /** What was on hand after it. */
    onHand: Integer;
    reason?: string;
    reference?: string;
    /** The claim a commit or a return was for. */
    holder?: {
      schema: Named;
      id: Named;
    };
    /** The claim's hold. */
    hold?: string;
    /** The item's index in its hold. */
    item?: Integer;
    /** The subject of the principal whose call moved it. */
    actor: string;
    /** When, in epoch milliseconds. */
    at: Integer;
  }[];
  /** The cursor of the next page; null after the last. */
  next: string | null;
}

/** The codes Stock's refusals carry. */
enum StockVeto {
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
   * holdFor of more units than the config's maxPerHold lets one item hold
   * (details: asked, max).
   */
  over_max,
  /**
   * An adjustment or a count that leaves fewer units on hand than claims
   * hold, by a caller without the permission's .override for a count
   * (details: onHand, held).
   */
  below_held,
}

/**
 * Counts units of what the instance stands for: a product's inventory, a
 * ticket type, a coupon's redemptions. Stock keeps the units on hand and the
 * units claims hold, and every change of the count on hand is a movement with
 * a reason, the claim it was for and who made it. A claim takes units only
 * through the supply contract's operations, which Holds invokes by name:
 * holdFor holds what an item of the claim asks, commitFor takes held units
 * out of what is on hand (they are sold), releaseFor frees them, and
 * returnFor puts committed units back. Each checks the claim's own
 * Holds.items, so no caller holds or frees units by calling one directly, and
 * each is idempotent. A hold that does not fit what is available is refused,
 * unless the config allows backorder; checkHold counts what the claim's other
 * items of the instance ask. A deleted claim's committed units stay sold. A
 * caller with the config's permission receives, adjusts and counts stock.
 * With a price, holdFor returns the unit price and its currency, and the
 * claim keeps the price each item was held at. An instance is not deleted
 * while it holds units for a claim. Stock and Calendar declare the same
 * contract operations, so a type composes one of them.
 */
@declaresBehavior({ conflicts: ["Calendar"], vetoes: StockVeto })
export abstract class Stock<C extends StockConfig> implements /** Units a claim holds: holdFor, commitFor, releaseFor and returnFor, which Holds calls on the row, and checkHold, which it asks of the row before a take. */ Supply {
  /**
   * The units on hand: received, adjusted and counted, less what claims
   * committed, plus what they returned. Always the sum of the instance's
   * movements.
   */
  onHand: Generic.Int64;
  /** The units claims hold and have neither committed nor released. */
  held: Generic.Int64;
  /**
   * onHand less held: what a new hold can take. Never below 0 unless the
   * config allows backorder.
   */
  @computed available: unknown;
  /**
   * Whether available is at or below the config's lowAt; false without one. A
   * list filters on it, so where Stock.low is true lists what to reorder.
   */
  @volatile low: unknown;

  /**
   * Adds units that arrived to what is on hand, with a reason and an optional
   * reference, such as a delivery note's. Needs the config's permission.
   * @writes
   */
  abstract receiveStock(params: ReceiveStockParams): ReceiveStockResult;

  /**
   * Adds a delta, positive or negative, to what is on hand, with a reason:
   * damage, loss, a correction. Needs the config's permission. Refused when
   * it leaves fewer on hand than claims hold, unless the config allows
   * backorder.
   * @writes
   */
  abstract adjustStock(params: AdjustStockParams): AdjustStockResult;

  /**
   * Sets what is on hand to a count, with a reason, recording the difference
   * as a movement; a count equal to what is on hand records nothing. Needs
   * the config's permission, and its .override for a count below what claims
   * hold.
   * @writes
   */
  abstract countStock(params: CountStockParams): CountStockResult;

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
   * Takes back units an item of a claim committed, once the claim's item is
   * returned.
   * @writes
   */
  abstract returnFor(params: ReturnForParams): ReturnForResult;

  /**
   * Answers whether holdFor would hold what an item asks now, and when not,
   * why.
   */
  abstract checkHold(params: CheckHoldParams): CheckHoldResult;

  /**
   * Lists the instance's movements, newest first, one page at a time: each
   * change of what is on hand, by kind, with its delta, reason, the claim and
   * item it was for, who made it and when.
   */
  abstract listStockMovements(params: ListStockMovementsParams): ListStockMovementsResult;

  /**
   * On a table, the supply's record of each claim item it holds or held,
   * keyed by the claim's type and key, the hold and the item's index: what
   * the item asked, its state, which a call moves only forward, so a call
   * made again changes nothing, and the price it was held at. A deleted
   * claim's records retire under a key of their own: what it held is
   * released, what it committed stays sold, and a claim created again with
   * its key holds afresh.
   */
  @subTable({ key: "record" })
  static holds = class {
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
    /** The units the item asked. */
    quantity: Generic.Int64;
    /** held, committed, released or returned. */
    state: string;
    /** The unit price it was held at, in the currency's minor units. */
    unitPrice: Generic.Int64;
    /** The unit price's currency. */
    currency: string;
    /** When it last moved, in epoch milliseconds. */
    at: Temporal.DateTime;
    /** Whether it is a deleted claim's. */
    retired: boolean;
  };

  /**
   * On a table, every change of what is on hand, append-only, keyed by its
   * sequence from 1: its kind, its delta, what was on hand after it, its
   * reason and reference, the claim and item it was for, who made it and
   * when. onHand always equals the sum of the deltas.
   */
  @subTable({ key: "seq" })
  static movements = class {
    /** The movement's sequence on the row, from 1, as decimal text. */
    seq: string;
    /** receive, adjust, count, commit or return. */
    kind: string;
    /** What it added to what is on hand, below 0 for what it took away. */
    delta: Generic.Int64;
    /** What was on hand after it. */
    onHand: Generic.Int64;
    /** Why, as the caller gave it; absent for a claim's. */
    reason: string;
    /** A reference the caller gave, such as a delivery note's. */
    reference: string;
    /** For a commit or a return, the claim's type. */
    holderSchema: string;
    /** For a commit or a return, the claim's key. */
    holderId: string;
    /** For a commit or a return, the hold. */
    hold: string;
    /** For a commit or a return, the item's index. */
    item: Generic.Int64;
    /** The subject of the caller whose command made it. */
    actor: string;
    /** When, in epoch milliseconds. */
    at: Temporal.DateTime;
  };
}
