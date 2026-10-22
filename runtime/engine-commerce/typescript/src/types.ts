/*
The commerce behaviors' configs, records and constants (D70): what a
schema gives Stock, Holds and Payments, what their fields hold, and what their
operations return. The behaviors are Rust (runtime/engine-commerce/rust,
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
  /** The sum of the prices when every item has one in one currency; null otherwise. */
  readonly total: { readonly amount: number; readonly currency: string } | null;
}

/** checkHold's answer, and listHolds' check of a pending item. */
export interface HoldCheck {
  readonly fits: boolean;
  readonly code?: string;
  readonly reason?: string;
  readonly details?: Readonly<Record<string, unknown>>;
}

/** What a move into a state waits for under Payments' require. */
export type PaymentRequirement = 'authorized' | 'paid';

/** Payments' config. */
export interface PaymentsConfig {
  /** What the instance owes: an own integer field, or the claim's Holds.total, which carries its currency. */
  readonly amount: FieldRef | { readonly holds: true };
  /** The currency of an amount from a field: an ISO 4217 code, or an own Finance.Currency field. */
  readonly currency?: CurrencySource;
  /** automatic when absent: a provider captures what it authorizes; manual: captureOn states capture it. */
  readonly capture?: 'automatic' | 'manual';
  readonly captureOn?: readonly string[];
  /** A move into each state waits until what is due is authorized, or paid. */
  readonly require?: Readonly<Record<string, PaymentRequirement>>;
  /** States whose entry voids the attempts still open. */
  readonly voidOn?: readonly string[];
  /** Money captured in a voidOn state: kept for an operator (keep, the default), or refunded. */
  readonly afterRelease?: 'keep' | 'refund';
  /** What refundPayment needs; refundPayment is refused without it. */
  readonly refunds?: { readonly permission: string };
  /** What recordPaymentEvent, listPaymentActions and settleRefundFor need: the provider's adapter's. */
  readonly record: { readonly permission: string };
  /** On a refund instance: the link to the instance it refunds. */
  readonly refundOf?: { readonly link: string };
  /** On a refund instance: the states whose entry asks the refund. */
  readonly issueOn?: readonly string[];
}

/** An attempt's status at its provider; it only moves forward. */
export type PaymentAttemptStatus = 'open' | 'requiresAction' | 'processing' | 'authorized' | 'captured' | 'failed' | 'cancelled';

/** Payments.state. */
export type PaymentsState = 'unpaid' | 'pending' | 'authorized' | 'paid' | 'partiallyRefunded' | 'refunded';

/** One element of Payments.attempts, newest first. */
export interface PaymentAttempt {
  readonly attempt: string;
  readonly provider: string;
  readonly providerRef?: string;
  readonly amount: number;
  readonly currency: string;
  readonly status: PaymentAttemptStatus;
  readonly authorized?: number;
  readonly captured?: number;
  readonly brand?: string;
  readonly last4?: string;
  readonly openedAt: number;
  readonly updatedAt: number;
}

/** Payments' fields, as a read returns them under behaviors.Payments; amounts in the currency's minor units. */
export interface PaymentsRecord {
  /** What is owed; 0 while a Holds.total cannot be read, when openPayment and require refuse with total_unknown. */
  readonly due: number;
  readonly authorized: number;
  readonly captured: number;
  readonly refunded: number;
  /** due less captured, never below 0. */
  readonly outstanding: number;
  /** What captures brought past what was due, which Payments asked back with a refund. */
  readonly overpaid: number;
  readonly currency?: string;
  readonly state: PaymentsState;
  /** The last twenty attempts, newest first. */
  readonly attempts: readonly PaymentAttempt[];
}

/** What an action asks a provider's adapter to do. */
export type PaymentActionKind = 'create' | 'capture' | 'void' | 'refund';

/** One action listPaymentActions returns: its id is the idempotency key the adapter sends the provider. */
export interface PaymentAction {
  readonly action: string;
  readonly kind: PaymentActionKind;
  readonly attempt: string;
  readonly amount: number;
  readonly currency: string;
  /** The instance whose action it is. */
  readonly id: string;
  readonly at: number;
  readonly providerRef?: string;
  readonly refund?: string;
  readonly returnUrl?: string;
}

/** What a provider event reports, as recordPaymentEvent takes it. */
export type PaymentEventKind = 'requiresAction' | 'processing' | 'authorized' | 'captured' | 'failed' | 'cancelled' | 'refunded' | 'refundFailed';
