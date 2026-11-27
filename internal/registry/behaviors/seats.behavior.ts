// Seats's class (D103): the build derives seats.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { Temporal } from "superscalar";
import { declaresBehavior, subTable, type Integer, type JsonSchema } from "@superschematic/db";

/** A state's name, as a config writes it. */
type StateName = JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_-]*$"; maxLength: 64 }>;

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface SeatsConfig {
  /** The seats, by name. */
  seats: JsonSchema<Record<StateName, {
    /**
     * How many subjects the seat holds at most, the subject its fromField
     * holds among them; no cap when absent.
     */
    max?: JsonSchema<Integer, { minimum: 1 }>;
    /**
     * What a take of a full seat does: refuse, the veto seat_full, or
     * displace, which unseats the subject seated longest that the seat's
     * fromField does not hold; refuse when absent. Needs max.
     */
    onFull?: "refuse" | "displace";
    /**
     * The permission a caller needs to seat itself; any caller may when
     * absent.
     */
    take?: Named;
    /**
     * The permission a caller needs to seat or unseat another subject; no
     * caller may when absent. A caller leaves a seat it holds without it.
     */
    assign?: Named;
    /**
     * An own field that holds a subject (string, Identity.UUID or
     * Identity.UserID): the subject it holds is seated, counts toward max, is
     * never displaced, and cannot leave the seat while the field holds them.
     */
    fromField?: Named;
  }>, { minProperties: 1 }>;
}

interface TakeSeatParams {
  /** The seat to take. */
  seat: Named;
  /**
   * The subject to seat; the caller's own when absent. Seating another needs
   * the seat's assign permission.
   */
  subject?: Named;
}

interface TakeSeatResult {
  /** The seat. */
  seat: string;
  /** The subject seated, a hyphenated UUID in base62. */
  subject: string;
  /**
   * The subjects the take unseated to make room, longest seated first; [] for
   * none.
   */
  displaced: string[];
  /**
   * True when the subject was seated already, so nothing was written; absent
   * otherwise.
   */
  unchanged?: boolean;
}

interface LeaveSeatParams {
  /** The seat to leave. */
  seat: Named;
  /**
   * The subject to unseat; the caller's own when absent. Unseating another
   * needs the seat's assign permission.
   */
  subject?: Named;
}

interface LeaveSeatResult {
  /** The seat. */
  seat: string;
  /** The subject unseated, a hyphenated UUID in base62. */
  subject: string;
  /**
   * True when the subject was not seated, so nothing was written; absent
   * otherwise.
   */
  unchanged?: boolean;
}

interface ListSeatsParams {
  /** Only this seat's subjects. */
  seat?: Named;
}

interface ListSeatsResult {
  items: {
    /** The seat. */
    seat: string;
    /** The subject seated. */
    subject: string;
    /**
     * When it was seated, in epoch milliseconds; null for the subject the
     * seat's fromField holds with no row of its own.
     */
    seatedAt: Integer | null;
    /** The subject of the caller who seated it; null as seatedAt is. */
    seatedBy: string | null;
    /** Whether the seat's fromField holds the subject. */
    fromField: boolean;
  }[];
}

/** The codes Seats's refusals carry. */
enum SeatsVeto {
  /**
   * takeSeat of a seat that holds its max, whose onFull is refuse or whose
   * every occupant its fromField holds, or an update that gives the fromField
   * of a seat whose onFull is refuse a subject not seated there, past its
   * max; details give the seat, its max and the subjects seated.
   */
  seat_full,
  /**
   * leaveSeat of the subject the seat's fromField holds, who leaves only when
   * the field holds another; details give the seat, the subject and the
   * field.
   */
  held_by_field,
}

/**
 * Named seats on the row, each holding the subjects seated in it. A caller
 * takes a seat for itself, or seats and unseats another, and leaves a seat it
 * holds without a permission. A seat may cap how many it holds (max), and a
 * take of a full seat is refused, or unseats the subject seated longest
 * (onFull). A seat may seat the subject an own field holds (fromField), who
 * counts in it and cannot leave it while the field holds them. A Workflow
 * transition's by and a StateRules state's by name a seat as seat:<name>,
 * which a caller seated in it passes. The behaviors' core alone implements
 * it, on a table; no engine type composes it.
 */
@declaresBehavior({ vetoes: SeatsVeto })
export abstract class Seats<C extends SeatsConfig> {
  /**
   * Seats a subject in a seat: the caller, with the seat's take permission
   * when it names one, or another, with its assign permission. A full seat
   * refuses it, or unseats the subject seated longest when its onFull is
   * displace. A subject seated already is answered unchanged.
   * @writes
   * @checks seats.*.take seats.*.assign
   */
  abstract takeSeat(params: TakeSeatParams): TakeSeatResult;

  /**
   * Unseats a subject from a seat: the caller, which needs no permission, or
   * another, with the seat's assign permission. The subject the seat's
   * fromField holds cannot leave while it holds them. A subject not seated is
   * answered unchanged.
   * @writes
   * @checks seats.*.assign
   */
  abstract leaveSeat(params: LeaveSeatParams): LeaveSeatResult;

  /**
   * Lists the subjects seated on the row, by seat in code point order, each
   * seat's fromField subject first and then the others in the order they were
   * seated; with seat, only its subjects.
   */
  abstract listSeats(params: ListSeatsParams): ListSeatsResult;

  /**
   * One row per subject seated on the row, keyed by its number, the order
   * they were seated in: the seat, the subject, and when and by whom it was
   * seated. A leave or a displacement deletes its row; the subject a seat's
   * fromField holds needs none.
   */
  @subTable({ key: "number" })
  static occupants = class {
    /**
     * The occupant's number on the row, one past the greatest when it was
     * seated, in ten digits zero-padded so the key's order is theirs.
     */
    number: string;
    /** The seat. */
    seat: string;
    /** The subject seated, a hyphenated UUID kept in base62. */
    subject: string;
    /** When it was seated, in epoch milliseconds. */
    seatedAt: Temporal.DateTime;
    /**
     * The subject of the caller who seated it: the subject itself, or the one
     * who assigned it.
     */
    seatedBy: string;
  };
}
