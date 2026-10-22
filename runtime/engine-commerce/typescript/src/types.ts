/*
The commerce behaviors' configs, records and constants (D70): what a
schema gives Stock, Calendar and Holds, what their fields hold, and what
their operations return. The behaviors are Rust (runtime/engine-commerce/rust,
D58), in the engine's addon.
*/

/** The most items a claim's holds ask together when Holds' config gives no maxItems. */
export const DEFAULT_MAX_ITEMS = 100;

/** An own field of the type, by JSON key. */
export interface FieldRef {
  readonly field: string;
}

/** A currency: an ISO 4217 code, or an own Finance.Currency field that holds one. */
export type CurrencySource = string | FieldRef;

/** Stock's config. */
export interface StockConfig {
  /** What receiving, adjusting and counting need; a count below what claims hold needs its `.override`. */
  readonly permission: string;
  /** The unit price holdFor returns: an own integer field, in the currency's minor units, and its currency. */
  readonly price?: { readonly field: string; readonly currency: CurrencySource };
  /** Stock.low is true while available is at or below this: a count or an own integer field. */
  readonly lowAt?: number | FieldRef;
  /** The most units one item of a claim holds. */
  readonly maxPerHold?: number;
  /** Whether a hold may take more than is available. */
  readonly backorder?: boolean;
}

/** Stock's fields, as a read returns them under behaviors.Stock. */
export interface StockRecord {
  readonly onHand: number;
  readonly held: number;
  /** onHand less held, never below 0 unless backorder. */
  readonly available: number;
  /** Whether available is at or below lowAt, by the config as it is now. */
  readonly low: boolean;
}

/** What receiveStock, adjustStock and countStock return. */
export interface StockChange {
  readonly onHand: number;
  readonly held: number;
  readonly available: number;
  /** The movement's sequence number; null when the call changed nothing. */
  readonly movement: number | null;
}

/** An instance by schema and id. */
export interface InstanceRef {
  readonly schema: string;
  readonly id: string;
}

/** What moved a Stock count. */
export type StockMovementKind = 'receive' | 'adjust' | 'count' | 'commit' | 'return';

/** One movement, as listStockMovements returns it. */
export interface StockMovement {
  readonly seq: number;
  readonly kind: StockMovementKind;
  readonly delta: number;
  /** What was on hand after it. */
  readonly onHand: number;
  readonly reason?: string;
  readonly reference?: string;
  /** The claim a commit or a return was for, with its hold and item. */
  readonly holder?: InstanceRef;
  readonly hold?: string;
  readonly item?: number;
  readonly actor: string;
  readonly at: number;
}

/** A hold of Holds' config: a link, or an own list field's elements, each asking a quantity or an interval. */
export type HoldConfig = (
  | { readonly link: string }
  | { readonly each: { readonly field: string; readonly target: string }; readonly schema: string }
) &
  (
    | { readonly quantity: number | FieldRef }
    | { readonly interval: { readonly start: string } & ({ readonly end: string } | { readonly durationMs: number }) }
  );

/** Holds' config. */
export interface HoldsConfig {
  readonly holds: Readonly<Record<string, HoldConfig>>;
  /** The states whose entry takes the holds; the create alone when absent. */
  readonly takeOn?: readonly string[];
  readonly commitOn?: readonly string[];
  readonly releaseOn?: readonly string[];
  readonly returnOn?: readonly string[];
  /** The most items the holds ask together; DEFAULT_MAX_ITEMS when absent. */
  readonly maxItems?: number;
}

/** An item's state: it only advances, pending, held, committed, returned, or pending or held to released. */
export type HoldState = 'pending' | 'held' | 'committed' | 'released' | 'returned';

/** One element of Holds.items. */
export interface HoldItem {
  readonly hold: string;
  /** Its index in its hold: its element's in an each hold's list, 0 for a link hold. */
  readonly item: number;
  /** The supply's schema and id. */
  readonly schema: string;
  readonly id: string;
  readonly quantity?: number;
  readonly startMs?: number;
  readonly endMs?: number;
  readonly state: HoldState;
  /** The unit price the supply held it at, in the currency's minor units. */
  readonly unitPrice?: number;
  readonly currency?: string;
}

/** Holds' fields, as a read returns them under behaviors.Holds. */
export interface HoldsRecord {
  readonly items: readonly HoldItem[];
  /** The least advanced item's state; none before any is asked. */
  readonly state: 'none' | HoldState;
  /**
   * The sum of the priced items' unit prices times their quantities (1 for
   * time). An item without a price adds nothing; with none priced it is
   * `{ amount: 0, currency: null }`. Null only when the priced items'
   * currencies differ or the sum passes what a number holds exactly.
   */
  readonly total: { readonly amount: number; readonly currency: string | null } | null;
}

/** checkHold's answer, and listHolds' check of a pending item. */
export interface HoldCheck {
  readonly fits: boolean;
  readonly code?: string;
  readonly reason?: string;
  readonly details?: Readonly<Record<string, unknown>>;
}

/** The longest window listBookings and findAvailability read: 31 days. */
export const MAX_WINDOW_MS = 31 * 24 * 60 * 60 * 1000;

/** A day's opening hours: [open, close] wall times, HH:MM from 00:00 to 24:00. */
export type OpeningRanges = ReadonlyArray<readonly [string, string]>;

/** Weekly opening hours by day; a day absent or empty is closed. */
export type OpeningHours = Readonly<Partial<Record<'mon' | 'tue' | 'wed' | 'thu' | 'fri' | 'sat' | 'sun', OpeningRanges>>>;

/** Calendar's config. */
export interface CalendarConfig {
  /** The IANA time zone the hours are wall times in. */
  readonly timeZone: string;
  readonly hours?: OpeningHours;
  /** How many holds may overlap at any instant: a count, or an own integer field; 1 when absent. */
  readonly capacity?: number | FieldRef;
  /** The start grid: a hold starts a whole number of slots after an opening time; any whole minute when absent. */
  readonly slotMs?: number;
  /** The shortest hold; slotMs, or a minute, when absent. */
  readonly minMs?: number;
  /** The longest hold; a day when absent. */
  readonly maxMs?: number;
  /** Time kept free around each hold. */
  readonly bufferMs?: { readonly before?: number; readonly after?: number };
  /** How long ahead of its start a hold must be made, by the engine's clock. */
  readonly leadMs?: number;
  /** How far ahead a hold may start. */
  readonly horizonMs?: number;
  /** What setOpeningHours and the closures need; nobody may when absent. */
  readonly permission?: string;
}

/** A closure of an instance, as addClosure returns it. */
export interface CalendarClosure {
  readonly closure: string;
  readonly startMs: number;
  readonly endMs: number;
  readonly reason: string;
}

/** Calendar's fields, as a read returns them under behaviors.Calendar. */
export interface CalendarRecord {
  readonly timeZone: string;
  readonly hours: OpeningHours;
  /** The first hundred closures that have not ended, soonest first. */
  readonly closures: readonly CalendarClosure[];
}

/** One hold of an instance, as listBookings returns it. */
export interface CalendarBooking {
  readonly holder: InstanceRef;
  readonly hold: string;
  readonly item: number;
  readonly startMs: number;
  readonly endMs: number;
  readonly state: 'held' | 'committed';
  /** True for a deleted claim's committed time, which stays booked. */
  readonly retired?: boolean;
}

/** One slot findAvailability found. */
export interface CalendarSlot {
  readonly id: string;
  readonly startMs: number;
  readonly endMs: number;
  /** How many more holds the instance can take then. */
  readonly remaining: number;
}

/** What findAvailability returns. */
export interface CalendarAvailability {
  readonly slots: readonly CalendarSlot[];
  /** Whether more slots fit than limit returned, more instances matched than were read, or the search stopped at its bound of work. */
  readonly truncated: boolean;
}
