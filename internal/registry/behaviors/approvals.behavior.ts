// Approvals' class (D103): the build derives approvals.behavior.json from
// it (make behaviors), which the behaviors' core and the engine read.
import { Temporal } from "superscalar";
import { computed, declaresBehavior, subTable, type Integer, type JsonSchema } from "@superschematic/db";

/** A state's name, as a config writes it. */
type StateName = JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_-]*$"; maxLength: 64 }>;

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface ApprovalsConfig {
  /** The gates, by the state of the type's Workflow each gates. */
  gates: JsonSchema<Record<StateName, ApprovalsGate>, { minProperties: 1 }>;
  /**
   * Whether a change of the instance's own fields, an update or an
   * operation's, consumes every gate's sign-offs; true when absent.
   */
  resetOnChange?: boolean;
  /**
   * The field of the type whose Workflow the gates are of. Implied when the
   * type has one Workflow; needed when it has several. A Workflow anchored on
   * the type is named Workflow. A table's alone: an engine type refuses it.
   */
  status?: Named;
}

/** One gate: who signs off for its state, how many, and what consumes it. */
interface ApprovalsGate {
  /**
   * How many distinct principals must sign off before a transition into the
   * state.
   */
  count: JsonSchema<Integer, { minimum: 1; maximum: 100 }>;
  /** The permission a caller needs to sign off. */
  permission: Named;
  /**
   * The states of the type's Workflow a transition from is gated; every
   * state when absent.
   */
  from?: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  /**
   * Whether the principal who created the instance is refused a sign-off;
   * true when absent.
   */
  excludeCreator?: boolean;
  /**
   * The own fields whose change, by an update, consumes the gate's
   * sign-offs; [] for none. Absent, the gate follows resetOnChange. A
   * table's alone: an engine type refuses it.
   */
  resetOn?: JsonSchema<Named[], { maxItems: 64; uniqueItems: true }>;
  /**
   * Whether a sign-off names the row's version its signer read: signOff
   * then needs version, and one that is not the row's version is vetoed
   * stale. A table's alone: an engine type refuses it.
   */
  pinned?: boolean;
}

interface SignOffParams {
  /** The gated state to sign off for. */
  state: Named;
  /** Why, for the others. */
  note?: JsonSchema<string, { maxLength: 10000 }>;
  /**
   * The row's version the signer read, the version a write through the core
   * is held to. Needed on a pinned gate; on any gate, one that is not the
   * row's version is vetoed stale before anything else is checked. A table's
   * alone: an engine type refuses it.
   */
  version?: JsonSchema<Integer, { minimum: 1 }>;
}

interface SignOffResult {
  /** The gated state. */
  state: string;
  /** The subject of the caller who signed. */
  subject: string;
  note?: string;
  /** When, in epoch milliseconds. */
  at: Integer;
  /**
   * Every subject whose sign-off for the state is not yet consumed, in the
   * order they signed.
   */
  signedBy: string[];
  /** How many the gate needs. */
  count: Integer;
}

interface WithdrawSignOffParams {
  /** The gated state whose sign-off to withdraw. */
  state: Named;
}

interface WithdrawSignOffResult {
  /** The gated state. */
  state: string;
  /** The subject of the caller who signed. */
  subject: string;
  note?: string;
  /** When it was signed, in epoch milliseconds. */
  at: Integer;
}

interface ListSignOffsParams {
  /** Only sign-offs for this state. */
  state?: Named;
  /** At most this many sign-offs; 50 when absent. */
  limit?: JsonSchema<Integer, { minimum: 1; maximum: 500 }>;
  /** The next of the previous page. */
  cursor?: string;
}

interface ListSignOffsResult {
  items: ListedSignOff[];
  /** The cursor of the next page; null after the last. */
  next: string | null;
}

/** A sign-off as listSignOffs lists it. */
interface ListedSignOff {
  /** The gated state. */
  state: string;
  /** The subject of the caller who signed. */
  subject: string;
  note?: string;
  /** When, in epoch milliseconds. */
  at: Integer;
  /**
   * When a change of the own fields, entering the state or a new version
   * without its gate consumed it; absent while it counts.
   */
  consumedAt?: Integer;
}

/** The codes Approvals' refusals carry. */
enum ApprovalsVeto {
  /**
   * A transition into a gated state, from a state its gate allows, with fewer
   * sign-offs than its count; details give the state, the count required and
   * the sign-offs it has.
   */
  approvals_missing,
  /** signOff by a caller whose sign-off for the state is not yet consumed. */
  already_signed,
  /**
   * signOff by the principal who created the instance, on a gate that
   * excludes the creator.
   */
  creator_cannot_sign,
  /** signOff while the instance is in the gated state. */
  in_state,
  /** signOff or withdrawSignOff for a state the config does not gate. */
  no_gate,
  /**
   * withdrawSignOff by a caller with no sign-off for the state that is not
   * yet consumed.
   */
  not_signed,
  /**
   * signOff naming a version that is not the row's version: what the signer
   * read has moved since; details give the version it named and the row's. A
   * table's alone: an engine type refuses version.
   */
  stale,
}

/**
 * Sign-offs that gate states of the type's Workflow. A transition into a
 * gated state, from a state its gate allows, waits until as many distinct
 * principals holding the gate's permission as its count have signed off; the
 * instance's creator cannot sign unless the gate allows it. A change of the
 * instance's own fields consumes every gate's sign-offs, unless the config
 * keeps them, and entering a gated state consumes that gate's, so a return to
 * it signs again. Consumed sign-offs stay listed. Deleting the instance
 * deletes its sign-offs.
 */
@declaresBehavior({ requires: ["Workflow"], vetoes: ApprovalsVeto })
export abstract class Approvals<C extends ApprovalsConfig> {
  /**
   * The sign-offs not yet consumed, by gated state: { <state>: { signedBy }
   * }, each subject that signed, in the order they signed; a gate with none
   * is absent.
   */
  @computed gates: unknown;

  /**
   * Signs off on the instance for a gated state, as the caller. Needs the
   * gate's permission; refused for the instance's creator unless the gate
   * allows it, while the instance is in the state, and for a caller whose
   * sign-off for the state is not yet consumed.
   * @writes
   * @returns The sign-off, with the gate's sign-offs after it and the count
   * it needs.
   */
  abstract signOff(params: SignOffParams): SignOffResult;

  /**
   * Withdraws the caller's sign-off for a gated state, one not yet consumed.
   * @writes
   * @returns The sign-off withdrawn.
   */
  abstract withdrawSignOff(params: WithdrawSignOffParams): WithdrawSignOffResult;

  /**
   * Lists the instance's sign-offs, oldest first, one page at a time, the
   * consumed ones included; with state, only those for it.
   */
  abstract listSignOffs(params: ListSignOffsParams): ListSignOffsResult;

  /**
   * One row per sign-off on the row, keyed by its number, the order it was
   * given in: the gated state, the subject who signed, why, when, and when it
   * was consumed, absent while it counts. A withdrawal deletes its row;
   * consumed ones stay for listSignOffs.
   */
  @subTable({ key: "number" })
  static signOffs = class {
    /**
     * The sign-off's number on the row, from 1 in the order they were given,
     * in ten digits zero-padded so the key's order is theirs.
     */
    number: string;
    /** The gated state it signs off for. */
    state: string;
    /** The subject of the caller who signed. */
    subject: string;
    /** Why, for the others. */
    note: string;
    /** When it was given, in epoch milliseconds. */
    at: Temporal.DateTime;
    /**
     * When a change of the fields its gate names, an entry into the gated
     * state or a build without its gate consumed it; absent while it counts.
     */
    consumedAt: Temporal.DateTime;
  };
}
