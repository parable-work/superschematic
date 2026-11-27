// Payments's class (D103): the build derives payments.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { Generic } from "superscalar";
import { computed, declaresBehavior, subTable, type Integer, type JsonSchema } from "@superschematic/db";

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

type PaymentsConfig = JsonSchema<{
  /**
   * What the instance owes: an own integer field of the type, in the
   * currency's minor units ({ field }), or the claim's Holds.total ({ holds:
   * true }), which carries its currency.
   */
  amount: {
    /**
     * An own integer field of the type, by JSON key, such as a Finance.Money
     * field.
     */
    field: Named;
  } | {
    /** The type's Holds.total. */
    holds: true;
  };
  /**
   * The currency of an amount from a field: an ISO 4217 code, or an own
   * Finance.Currency field of the type ({ field }) that holds one.
   * Holds.total carries its own.
   */
  currency?: JsonSchema<JsonSchema<string, { pattern: "^[A-Z]{3}$" }>, { description: "An ISO 4217 code; the engine checks it is one." }> | {
    /** An own Finance.Currency field of the type, by JSON key. */
    field: Named;
  };
  /**
   * automatic: a provider captures what it authorizes. manual: captureOn
   * states capture it. automatic when absent.
   */
  capture?: JsonSchema<"automatic" | "manual", { type: "string" }>;
  /**
   * With capture manual, the states whose entry captures what is authorized.
   */
  captureOn?: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  /**
   * Per state of the type's Workflow, what a move into it waits for:
   * authorized, every amount due authorized or paid; paid, every amount due
   * captured.
   */
  require?: JsonSchema<Record<JsonSchema<string, { minLength: 1 }>, JsonSchema<"authorized" | "paid", { type: "string" }>>, { minProperties: 1 }>;
  /**
   * The states whose entry cancels the open attempts and voids the authorized
   * ones.
   */
  voidOn?: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  /**
   * What becomes of money captured while the instance is in a voidOn state,
   * such as a payment reported after a hold expired: refund records a refund
   * of it; keep leaves it for an operator. keep when absent.
   */
  afterRelease?: JsonSchema<"refund" | "keep", { type: "string" }>;
  /** Who refunds directly; nobody, but a refund instance, when absent. */
  refunds?: {
    /** The permission refundPayment needs. */
    permission: Named;
  };
  /** Who reports what providers did: the adapter's principal. */
  record: {
    /**
     * The permission recordPaymentEvent needs: the adapter's service holds
     * it.
     */
    permission: Named;
  };
  /**
   * Makes the instance a refund of the instance a link names: its amount is
   * refunded from that instance's payments.
   */
  refundOf?: {
    /**
     * A required, single-valued link of the type's Links config to the
     * instance refunded, which composes Payments.
     */
    link: JsonSchema<string, { pattern: "^[a-z][A-Za-z0-9]{0,63}$" }>;
  };
  /**
   * With refundOf, the states whose entry asks the linked instance to refund
   * the amount.
   */
  issueOn?: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
}, { allOf: [{ if: { required: ["amount"]; properties: { amount: { required: ["field"] } } }; then: { required: ["currency"] } }, { if: { required: ["refundOf"] }; then: { required: ["issueOn"] } }, { if: { required: ["issueOn"] }; then: { required: ["refundOf"] } }, { if: { required: ["captureOn"] }; then: { required: ["capture"]; properties: { capture: { const: "manual" } } } }] }>;

interface OpenPaymentParams {
  /**
   * The provider's name, as the adapter that serves it knows it, such as
   * stripe.
   */
  provider: JsonSchema<string, { pattern: "^[a-z][a-z0-9_-]{0,63}$" }>;
  /**
   * The caller's key: a repeat with the same key returns what the first call
   * did.
   */
  idempotencyKey: JsonSchema<string, { pattern: "^[A-Za-z0-9._:-]{1,255}$" }>;
  /** Where the provider sends the payer back to, when it does. */
  returnUrl?: JsonSchema<string, { format: "uri"; maxLength: 2048 }>;
}

interface OpenPaymentResult {
  /** The attempt's id: the idempotency key the adapter sends the provider. */
  attempt: string;
  provider: string;
  amount: Integer;
  currency: string;
  status: JsonSchema<"open" | "requiresAction" | "processing" | "authorized" | "captured" | "failed" | "cancelled", { type: "string" }>;
}

interface RecordPaymentEventParams {
  /**
   * The provider's name, as the adapter that serves it knows it, such as
   * stripe.
   */
  provider: JsonSchema<string, { pattern: "^[a-z][a-z0-9_-]{0,63}$" }>;
  /** The provider's id of the event. */
  eventId: JsonSchema<string, { pattern: "^[A-Za-z0-9._:-]{1,255}$" }>;
  /** The attempt's id. */
  attempt?: JsonSchema<string, { minLength: 1; maxLength: 255 }>;
  /** The provider's id of the payment. */
  providerRef?: JsonSchema<string, { pattern: "^[A-Za-z0-9_-]{1,255}$" }>;
  /** For a refund's outcome: the refund's id. */
  refund?: JsonSchema<string, { minLength: 1; maxLength: 255 }>;
  /** What happened. */
  kind: JsonSchema<"requiresAction" | "processing" | "authorized" | "captured" | "failed" | "cancelled" | "refunded" | "refundFailed", { type: "string" }>;
  /** In the currency's minor units. */
  amount: JsonSchema<Integer, { minimum: 0; maximum: 9007199254740991 }>;
  /** An ISO 4217 code; the engine checks it is one. */
  currency: JsonSchema<string, { pattern: "^[A-Z]{3}$" }>;
  /** When the provider says it happened, in epoch milliseconds. */
  at: Integer;
  /** A card's brand, for display. */
  brand?: JsonSchema<string, { pattern: "^[a-z][a-z0-9_]{0,31}$" }>;
  /** A card's last four digits, for display. */
  last4?: JsonSchema<string, { pattern: "^[0-9]{4}$" }>;
  /** At most ten string pairs the adapter keeps with the event. */
  metadata?: JsonSchema<Record<JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_]{0,39}$" }>, JsonSchema<string, { maxLength: 500 }>>, { maxProperties: 10 }>;
}

interface RecordPaymentEventResult {
  /** Whether the event moved anything; false for a repeat or a late event. */
  applied: boolean;
  /** The attempt's status after it, when it names one. */
  attempt: object | null;
  /** Payments.state after it. */
  state: string;
}

interface CapturePaymentParams {
  /** The attempt's id. */
  attempt: JsonSchema<string, { minLength: 1; maxLength: 255 }>;
  /** In the currency's minor units. */
  amount?: JsonSchema<Integer, { minimum: 1; maximum: 9007199254740991 }>;
}

interface CapturePaymentResult {
  /** The action's id: the idempotency key the adapter sends the provider. */
  action: string;
  kind: JsonSchema<"create" | "capture" | "void" | "refund", { type: "string" }>;
  attempt: string;
  amount: Integer;
  currency: string;
}

interface VoidPaymentParams {
  /** The attempt's id. */
  attempt: JsonSchema<string, { minLength: 1; maxLength: 255 }>;
}

interface VoidPaymentResult {
  /** The action's id: the idempotency key the adapter sends the provider. */
  action: string;
  kind: JsonSchema<"create" | "capture" | "void" | "refund", { type: "string" }>;
  attempt: string;
  amount: Integer;
  currency: string;
}

interface RefundPaymentParams {
  /** In the currency's minor units. */
  amount: JsonSchema<Integer, { minimum: 1; maximum: 9007199254740991 }>;
  /** Why, for the record. */
  reason: JsonSchema<string, { minLength: 1; maxLength: 500 }>;
  /**
   * The caller's key: a repeat with the same key returns what the first call
   * did.
   */
  idempotencyKey: JsonSchema<string, { pattern: "^[A-Za-z0-9._:-]{1,255}$" }>;
}

interface RefundPaymentResult {
  /** The refund's id. */
  refund: string;
  /** The action's id: the idempotency key the adapter sends the provider. */
  action: string;
  amount: Integer;
  currency: string;
  status: JsonSchema<"requested" | "sent" | "succeeded" | "failed", { type: "string" }>;
}

interface ListPaymentActionsParams {
  /**
   * The provider's name, as the adapter that serves it knows it, such as
   * stripe.
   */
  provider: JsonSchema<string, { pattern: "^[a-z][a-z0-9_-]{0,63}$" }>;
  /** At most this many; 50 when absent. */
  limit?: JsonSchema<Integer, { minimum: 1; maximum: 500 }>;
  /** The next of the previous page. */
  cursor?: string;
}

interface ListPaymentActionsResult {
  items: {
    /**
     * The action's id: the idempotency key the adapter sends the provider.
     */
    action: string;
    kind: JsonSchema<"create" | "capture" | "void" | "refund", { type: "string" }>;
    attempt: string;
    amount: Integer;
    currency: string;
    /** The instance. */
    id: string;
    /** When it was recorded, in epoch milliseconds. */
    at: Integer;
    providerRef?: string;
    refund?: string;
    returnUrl?: string;
  }[];
  /** The cursor of the next page; null after the last. */
  next: string | null;
}

interface RefundForParams {
  /** The refund instance: its schema and id. */
  refund: {
    schema: Named;
    id: Named;
  };
  /** In the currency's minor units. */
  amount: JsonSchema<Integer, { minimum: 1; maximum: 9007199254740991 }>;
  /**
   * The caller's key: a repeat with the same key returns what the first call
   * did.
   */
  idempotencyKey: JsonSchema<string, { pattern: "^[A-Za-z0-9._:-]{1,255}$" }>;
}

interface RefundForResult {
  /** The refund's id here. */
  refund: string;
  action: string;
  amount: Integer;
  currency: string;
}

interface SettleRefundForParams {
  /** The instance refunded: its schema and id. */
  of: {
    schema: Named;
    id: Named;
  };
  /** The refund's id there. */
  refund: JsonSchema<string, { minLength: 1; maxLength: 255 }>;
  status: JsonSchema<"succeeded" | "failed", { type: "string" }>;
  /** In the currency's minor units. */
  amount: JsonSchema<Integer, { minimum: 0; maximum: 9007199254740991 }>;
}

interface SettleRefundForResult {
  /** Payments.state after it. */
  state: string;
}

/** The codes Payments's refusals carry. */
enum PaymentsVeto {
  /** openPayment while nothing is outstanding. */
  nothing_due,
  /**
   * A change of what is due, its field or the holds it is the total of, while
   * an attempt is open.
   */
  amount_locked,
  /** An amount in another currency than the instance's. */
  currency_mismatch,
  /** A capture past what is authorized. */
  over_capture,
  /**
   * A refund above what is captured, less what is refunded and what refunds
   * already ask (details: refundable).
   */
  exceeds_refundable,
  /**
   * A move into a state require holds for paid, while not everything due is
   * captured.
   */
  unpaid,
  /**
   * A move into a state require holds for authorized, while not everything
   * due is authorized or captured.
   */
  unauthorized,
  /**
   * A move out of the instance's state, or a release, while an attempt is
   * processing.
   */
  payment_in_progress,
  /**
   * An attempt, a provider reference or a refund the instance does not have.
   */
  no_attempt,
  /**
   * refundFor from an instance whose refundOf link does not point here, or
   * settleRefundFor from one that is not the instance refunded, or with an
   * outcome that instance did not record.
   */
  not_linked,
  /**
   * An idempotencyKey used before with other parameters, or refundFor under a
   * key other than the one the refund instance's Payments derives, or while
   * its refund is asked or paid.
   */
  key_reused,
  /**
   * capturePayment or voidPayment of an attempt that is not authorized, or
   * not open, as the action needs.
   */
  attempt_closed,
  /** refundPayment on a type whose config names no refunds permission. */
  not_configured,
  /**
   * openPayment, or a move into a state require holds, while the amount comes
   * from Holds.total and the total cannot be read: an item is pending, the
   * priced items are in two currencies, or their sum is past what an integer
   * holds.
   */
  total_unknown,
}

/**
 * Keeps what the instance owes and how it is paid: a checkout, an order, a
 * booking's deposit, an invoice, or a refund of one. The amount due comes
 * from an own integer field or from Holds.total, in a currency. The engine
 * never calls a payment provider: openPayment records an attempt and an
 * action, an adapter outside the engine reads the actions waiting for its
 * provider through listPaymentActions, calls the provider with each action's
 * id as its idempotency key, and reports what happened, and every provider
 * event, through recordPaymentEvent, which applies each event once and moves
 * an attempt only forward. capturePayment, voidPayment and refundPayment
 * record actions too. require holds a move into a state until the instance is
 * authorized or paid, voidOn states void its open attempts, and no move
 * leaves a state while an attempt is processing, so an expiry does not let go
 * of what is being paid for. A refund that needs sign-off is an instance of
 * its own, whose Payments names the instance it refunds (refundOf): entering
 * an issueOn state asks that instance's refundFor, and the provider's answer
 * comes back through settleRefundFor. Payments stores no card data and no raw
 * provider event.
 */
@declaresBehavior({ vetoes: PaymentsVeto })
export abstract class Payments<C extends PaymentsConfig> {
  /**
   * What the instance owes, in the currency's minor units. It does not change
   * while an attempt is open. From Holds.total, it is 0 while the total
   * cannot be read (an item pending, priced items in two currencies, or a sum
   * past what an integer holds), and openPayment and require refuse with
   * total_unknown then.
   */
  due: Generic.Int64;
  /** What the provider has authorized and not voided or captured. */
  authorized: Generic.Int64;
  /** What the provider has captured. */
  captured: Generic.Int64;
  /** What the provider has refunded. */
  refunded: Generic.Int64;
  /** due less captured, never below 0. */
  outstanding: Generic.Int64;
  /**
   * What captures brought past what was due, such as a late capture of an
   * attempt that failed while a newer one paid, which Payments asked back
   * with a refund of it.
   */
  overpaid: Generic.Int64;
  /** The ISO 4217 code of every amount. */
  currency: string;
  /**
   * unpaid, pending (an attempt is open), authorized, paid, partiallyRefunded
   * or refunded. A rule hears it turn paid.
   */
  state: string;
  /**
   * The last twenty attempts, newest first: { attempt, provider,
   * providerRef?, amount, currency, status, authorized?, captured?, brand?,
   * last4?, openedAt, updatedAt }.
   */
  @computed attempts: unknown;

  /**
   * Opens a payment attempt for what is outstanding at a provider, and
   * records the action that creates it there, for the provider's adapter;
   * returns the attempt and its amount. The same idempotencyKey returns the
   * same attempt. While an attempt is open, the amount due does not change.
   * @writes
   */
  abstract openPayment(params: OpenPaymentParams): OpenPaymentResult;

  /**
   * Records what a provider did, as its adapter reports it: an attempt's
   * progress, authorization, capture, failure or cancellation, or a refund's
   * outcome. Needs the config's record permission. Each (provider, eventId)
   * applies once, and a repeat changes and writes nothing. An attempt's
   * status only moves forward, so an event that arrives late is recorded and
   * changes nothing.
   * @writes
   */
  abstract recordPaymentEvent(params: RecordPaymentEventParams): RecordPaymentEventResult;

  /**
   * Records the action that captures an authorized attempt, all of it or an
   * amount, for the provider's adapter.
   * @writes
   */
  abstract capturePayment(params: CapturePaymentParams): CapturePaymentResult;

  /**
   * Records the action that cancels an open attempt or voids an authorized
   * one, for the provider's adapter.
   * @writes
   */
  abstract voidPayment(params: VoidPaymentParams): VoidPaymentResult;

  /**
   * Records a refund of an amount of what is captured and the action that
   * asks the provider for it. Needs the config's refunds permission. Refused
   * above what is captured, less what is refunded and what refunds already
   * ask. The same idempotencyKey returns the same refund.
   * @writes
   */
  abstract refundPayment(params: RefundPaymentParams): RefundPaymentResult;

  /**
   * Lists the actions waiting for a provider across the schema's instances,
   * oldest first, one page at a time: what its adapter is to do. An action
   * stops waiting when an event for it is recorded.
   * @scope schema
   */
  abstract listPaymentActions(params: ListPaymentActionsParams): ListPaymentActionsResult;

  /**
   * Records a refund that a refund instance asks of this one: its Payments'
   * refundOf link must point here, it must be in an issueOn state, the amount
   * must be its own and refundable, and it has no other refund asked or paid.
   * The refund instance's Payments invokes it as the instance enters an
   * issueOn state, under the idempotencyKey it derives from itself and its
   * failed refunds; another key is key_reused, and the same key returns the
   * same refund.
   * @writes
   */
  abstract refundFor(params: RefundForParams): RefundForResult;

  /**
   * Records on a refund instance the outcome of the refund it asked of the
   * instance it refunds: that instance's Payments invokes it when the
   * provider's refund event is reported there. Needs the config's record
   * permission, and refuses a status or an amount that is not what that
   * instance recorded of the refund.
   * @writes
   */
  abstract settleRefundFor(params: SettleRefundForParams): SettleRefundForResult;

  /**
   * On a table, each payment attempt the row opened, keyed by its id: its
   * provider and the provider's reference, its amount, currency and what was
   * due when it opened, its status, what is authorized and captured of it, a
   * card's brand and last four digits, and when it opened and last moved.
   */
  @subTable({ key: "attempt" })
  static attempts = class {
    /**
     * The attempt's id, pay_ and a key the command makes, which the adapter
     * sends its provider as an idempotency key.
     */
    attempt: string;
    /** Its place among the row's attempts, from 1. */
    seq: Generic.Int64;
    /** The key openPayment opened it under. */
    idempotencyKey: string;
    /** The provider it is at. */
    provider: string;
    /** The provider's reference, once the adapter reports it. */
    providerRef: string;
    /** What it asks, in the currency's minor units. */
    amount: Generic.Int64;
    /** Its ISO 4217 code. */
    currency: string;
    /** What was due when it opened. */
    due: Generic.Int64;
    /**
     * open, requiresAction, processing, authorized, captured, failed or
     * cancelled.
     */
    status: string;
    /** What the provider authorized of it. */
    authorized: Generic.Int64;
    /** What the provider captured of it. */
    captured: Generic.Int64;
    /** A card's brand, for display. */
    brand: string;
    /** A card's last four digits, for display. */
    last4: string;
    /** Where the provider sends the payer back. */
    returnUrl: string;
    /** When it opened, in epoch milliseconds. */
    openedAt: Generic.Int64;
    /** When it last moved, in epoch milliseconds. */
    updatedAt: Generic.Int64;
  };

  /**
   * On a table, each thing the provider's adapter is to do for the row
   * (create, capture, void, refund), keyed by its id, waiting until an event
   * for it is recorded.
   */
  @subTable({ key: "action" })
  static actions = class {
    /**
     * The action's id, act_ and a key the command makes, which the adapter
     * sends its provider as an idempotency key.
     */
    action: string;
    /** Its place among the row's actions, from 1. */
    seq: Generic.Int64;
    /** create, capture, void or refund. */
    kind: string;
    /** The attempt it acts on. */
    attempt: string;
    /** For a refund, the refund. */
    refund: string;
    /** The provider it is for. */
    provider: string;
    /** What it moves, in the currency's minor units. */
    amount: Generic.Int64;
    /** Its ISO 4217 code. */
    currency: string;
    /** When it was asked, in epoch milliseconds. */
    at: Generic.Int64;
    /** When an event for it was recorded; absent while it waits. */
    doneAt: Generic.Int64;
  };

  /**
   * On a table, each refund asked of the row, keyed by its id: the attempt it
   * draws on, its amount and reason, its status and what was refunded, and
   * the refund row that asked it, if one did.
   */
  @subTable({ key: "refund" })
  static refunds = class {
    /** The refund's id, rf_ and a key the command makes. */
    refund: string;
    /** Its place among the row's refunds, from 1. */
    seq: Generic.Int64;
    /** The key it was asked under. */
    idempotencyKey: string;
    /** The captured attempt it draws on. */
    attempt: string;
    /** That attempt's provider. */
    provider: string;
    /** What it asks, in the currency's minor units. */
    amount: Generic.Int64;
    /** Its ISO 4217 code. */
    currency: string;
    /** Why it was asked. */
    reason: string;
    /** requested, sent, succeeded or failed. */
    status: string;
    /** What the provider refunded. */
    refunded: Generic.Int64;
    /** The type of the refund row that asked it, through refundFor. */
    originSchema: string;
    /** The key of the refund row that asked it. */
    originId: string;
    /** When it was asked, in epoch milliseconds. */
    at: Generic.Int64;
    /** When it last moved, in epoch milliseconds. */
    updatedAt: Generic.Int64;
  };

  /**
   * On a refund row (refundOf), each refund it asked of the row it refunds,
   * keyed by the refund's id: its key, amount and currency, its status as the
   * provider settled it, and the Workflow entry it was asked at.
   */
  @subTable({ key: "refund" })
  static issues = class {
    /** The refund's id, as the row it refunds recorded it. */
    refund: string;
    /** Its place among the row's issues, from 1. */
    seq: Generic.Int64;
    /** The type of the row it refunds. */
    ofSchema: string;
    /** The key of the row it refunds. */
    ofId: string;
    /**
     * The key it was asked under: the refund row's type and key and the
     * issue's number.
     */
    idempotencyKey: string;
    /** What it asks, in the currency's minor units. */
    amount: Generic.Int64;
    /** Its ISO 4217 code. */
    currency: string;
    /** requested, sent, succeeded or failed. */
    status: string;
    /** What the provider refunded. */
    refunded: Generic.Int64;
    /**
     * The Workflow's enteredAt when it was asked, which tells a later entry
     * of an issueOn state from this one.
     */
    enteredAt: Generic.Int64;
    /** When it was asked, in epoch milliseconds. */
    at: Generic.Int64;
    /** When it last moved, in epoch milliseconds. */
    updatedAt: Generic.Int64;
  };

  /**
   * On a table, every provider event recorded, kept for the table as a whole
   * and keyed by its provider and event id, so an event applies once per
   * table and provider: the row it was recorded on, its normalised fields
   * alone, and whether it moved anything. A delete of the row leaves it, so a
   * redelivery is still a repeat.
   */
  @subTable({ key: "event", shared: true })
  static events = class {
    /** The provider and the event id, joined by /. */
    event: string;
    /** The provider. */
    provider: string;
    /** The provider's event id. */
    eventId: string;
    /** The key of the row it was recorded on. */
    rowKey: string;
    /** What it reported. */
    kind: string;
    /** Its amount, in the currency's minor units. */
    amount: Generic.Int64;
    /** Its ISO 4217 code. */
    currency: string;
    /** When the provider says it happened, in epoch milliseconds. */
    at: Generic.Int64;
    /** The attempt it reported on. */
    attempt: string;
    /** The provider's reference it named. */
    providerRef: string;
    /** The refund it reported on. */
    refund: string;
    /** Whether it moved anything. */
    applied: boolean;
    /** At most ten string pairs the adapter passed on. */
    metadata: Generic.JSON;
    /** When it was recorded, in epoch milliseconds. */
    recordedAt: Generic.Int64;
  };
}
