// Derived's class (D103): the build derives derived.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { Generic, Temporal } from "superscalar";
import { computed, declaresBehavior, subTable, volatile, type Integer, type JsonSchema } from "@superschematic/db";

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface DerivedConfig {
  /** The derived fields, by own top-level field. */
  fields: JsonSchema<Record<JsonSchema<string, { minLength: 1; maxLength: 64 }>, {
    /**
     * The own top-level fields the value is derived from, never the field
     * itself or another derived field.
     */
    from: JsonSchema<Named[], { minItems: 1; maxItems: 16; uniqueItems: true }>;
    /** What the model is asked, given the inputs. */
    instruction: JsonSchema<string, { minLength: 1; maxLength: 4000 }>;
    /**
     * A label the deployment maps to a provider's model, which the engine
     * only compares.
     */
    model: JsonSchema<string, { minLength: 1; maxLength: 200 }>;
    /**
     * The states of the type's Workflow in which the field is derived; every
     * state when absent.
     */
    states?: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  }>, { minProperties: 1; maxProperties: 16 }>;
  /** The permission a deriver needs to pull, settle and apply values. */
  permission: Named;
  /**
   * How many failed attempts a field takes before it waits for its inputs to
   * move; 3 when absent.
   */
  maxAttempts?: JsonSchema<Integer, { minimum: 1; maximum: 10 }>;
}

interface StaleDerivationsParams {
  /** Only this derived field. */
  field?: Named;
  /** At most this many, 50 when absent. */
  limit?: JsonSchema<Integer, { minimum: 1; maximum: 100 }>;
  /** The next of the previous page. */
  cursor?: string;
}

interface StaleDerivationsResult {
  items: {
    id: string;
    field: string;
    /**
     * The hash of the inputs, instruction, model and output, which a settle
     * presents.
     */
    sourceHash: string;
    /** Which attempt this is, from 1. */
    attempt: Integer;
    instruction: string;
    model: string;
    /** The inputs that hold a value, by field name. */
    inputs: object;
    /** The derived field's JSON Schema: what the value must be. */
    output: JsonSchema<unknown, { type: ["object", "boolean"] }>;
  }[];
  /** The cursor of the next page; null on the last. */
  next: string | null;
}

interface SettleDerivationsParams {
  items: JsonSchema<{
    id: Named;
    field: Named;
    sourceHash: Named;
    /** The value, which the derived field's type must accept. */
    value?: unknown;
    /** How sure the model is, from 0 to 1. */
    confidence?: JsonSchema<number, { minimum: 0; maximum: 1 }>;
    /** What the call cost, kept in the event's parameters for reports. */
    usage?: {
      inputTokens?: JsonSchema<Integer, { minimum: 0 }>;
      outputTokens?: JsonSchema<Integer, { minimum: 0 }>;
      costMicros?: JsonSchema<Integer, { minimum: 0 }>;
      modelId?: JsonSchema<string, { maxLength: 200 }>;
    };
    /** Why no value came back. */
    failure?: {
      class?: JsonSchema<string, { maxLength: 64 }>;
      message: JsonSchema<string, { maxLength: 2000 }>;
    };
  }[], { maxItems: 100 }>;
}

interface SettleDerivationsResult {
  /** How many values were written. */
  settled: Integer;
  /** How many failures were counted. */
  failed: Integer;
  skipped: {
    id: string;
    field: string;
    /**
     * not_found, not_derived, moved, overridden, out_of_state, invalid or
     * vetoed.
     */
    reason: string;
    /** For vetoed, the behavior that vetoed. */
    behavior?: string;
    /** For vetoed, its veto code. */
    code?: string;
    /** For invalid, what the type refuses. */
    issues?: unknown[];
  }[];
}

interface FailedDerivationsParams {
  /** At most this many, 50 when absent. */
  limit?: JsonSchema<Integer, { minimum: 1; maximum: 100 }>;
  /** The next of the previous page. */
  cursor?: string;
}

interface FailedDerivationsResult {
  items: {
    id: string;
    field: string;
    attempts: Integer;
    failure?: object;
  }[];
  next: string | null;
}

interface ApplyDerivedParams {
  fields: JsonSchema<Record<string, {
    sourceHash: Named;
    value: unknown;
    confidence?: JsonSchema<number, { minimum: 0; maximum: 1 }>;
    usage?: object;
  }>, { minProperties: 1 }>;
}

interface ApplyDerivedResult {
  /** The fields written, in the order given. */
  settled: string[];
}

interface RederiveParams {
  fields?: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
}

interface RederiveResult {
  /** The derived fields stale after it. */
  stale: string[];
}

/** The codes Derived's refusals carry. */
enum DerivedVeto {
  /** applyDerived or rederive of a field the config does not derive. */
  not_derived,
  /**
   * applyDerived with a hash its field's inputs, instruction, model or type
   * no longer give.
   */
  moved,
  /** applyDerived while the status is outside the field's states. */
  out_of_state,
  /**
   * applyDerived of a field a caller or another behavior's operation wrote;
   * rederive lets Derived write it again.
   */
  overridden,
}

/**
 * Own fields a model outside the engine fills from other own fields, under an
 * instruction the schema holds (D73). The engine calls no model: a deriver
 * holding the config's permission pulls the stale fields with
 * staleDerivations (each one's inputs, instruction, model label and the
 * field's JSON Schema, which is the output's) and settles its answers with
 * settleDerivations, presenting the hash it pulled. A value is written with
 * the operation's update(), so the type's validation and every guard judge
 * it, and its event attributes it to Derived with the confidence given. A
 * field is stale when its inputs, instruction, model or type moved since its
 * last value; that value stays until a new one settles. Once a caller, or
 * another behavior's operation, writes a derived field, Derived stops
 * deriving it until rederive. A failure appends no event; after maxAttempts
 * the field waits until its inputs move. Deleting the instance deletes what
 * Derived keeps of it.
 */
@declaresBehavior({ vetoes: DerivedVeto })
export abstract class Derived<C extends DerivedConfig> {
  /**
   * The last value settled for each derived field, by field: { sourceHash,
   * model, settledAt, settledBy, confidence? }, and overridden: { by, at }
   * once a caller or another behavior's operation wrote it. A field with
   * neither is absent.
   */
  @computed fields: unknown;
  /**
   * The derived fields whose inputs, instruction, model or type moved since
   * their last value, or that rederive asked for, and that no write overrode,
   * in the config's order.
   */
  @volatile stale: unknown;

  /**
   * Lists the derived fields a deriver should compute, oldest first: each
   * one's instance id, field, inputs by field name, instruction, model label,
   * the field's JSON Schema as output, the hash to present and the attempt
   * number. Leaves out a field outside its states, one overridden, one whose
   * inputs are all absent and one out of attempts. Needs the config's
   * permission.
   * @scope schema
   */
  abstract staleDerivations(params: StaleDerivationsParams): StaleDerivationsResult;

  /**
   * Settles what a deriver computed from staleDerivations: each item a value,
   * or a failure, with the hash it pulled. Values are written instance by
   * instance through applyDerived, each instance in one event; an item whose
   * hash moved, whose instance is gone, whose field was overridden or left
   * its states, whose value the type refuses, or that another behavior vetoes
   * is skipped, and the others still settle. A failure, an invalid value or a
   * veto counts an attempt and appends no event. An item given twice, or with
   * both a value and a failure, refuses the whole call. Needs the config's
   * permission.
   * @writes
   * @scope schema
   */
  abstract settleDerivations(params: SettleDerivationsParams): SettleDerivationsResult;

  /**
   * Lists the derived fields out of attempts, oldest first, with each one's
   * last failure. Needs the config's permission.
   * @scope schema
   */
  abstract failedDerivations(params: FailedDerivationsParams): FailedDerivationsResult;

  /**
   * Writes derived values on the instance, each with the hash it was computed
   * under: through update(), so the type's validation and every guard judge
   * them, attributed to Derived with each value's confidence. Refused for a
   * field the config does not derive, one a write overrode, one outside its
   * states and one whose hash moved. Needs the config's permission;
   * settleDerivations calls it.
   * @writes
   */
  abstract applyDerived(params: ApplyDerivedParams): ApplyDerivedResult;

  /**
   * Makes derived fields stale again, every one when fields is absent: clears
   * an override, the failed attempts, and the last value's hash, so the
   * deriver computes them anew. The values stay until new ones settle.
   * @writes
   */
  abstract rederive(params: RederiveParams): RederiveResult;

  /**
   * On a table, one row per derived field of the row, keyed by the field's
   * name: the hash its value is derived under, the hash of its last settled
   * value with its model, confidence, settler and time, the override a write
   * gave it, its failed attempts and last failure, and whether rederive
   * forced it. Every write of the row keeps them in line; a delete of the row
   * deletes them.
   */
  @subTable({ key: "field" })
  static derivations = class {
    /** The derived field's name. */
    field: string;
    /**
     * The SHA-256 of the model label, the instruction, the output schema and
     * the inputs that hold a value; absent while no input holds one.
     */
    sourceHash: string;
    /** The source hash of the last value settled; absent before the first. */
    settledHash: string;
    /** The model label the last value was settled under. */
    model: string;
    /** The confidence the last value was settled with, from 0 to 1. */
    confidence: number;
    /** When the last value was settled, in epoch milliseconds. */
    settledAt: Temporal.DateTime;
    /** The subject of the deriver that settled it. */
    settledBy: string;
    /**
     * Who wrote the field over Derived: a caller's subject, another
     * behavior's name, or existing for a value the row held before; absent
     * until one does and after rederive.
     */
    overriddenBy: string;
    /** When the override was written, in epoch milliseconds. */
    overriddenAt: Temporal.DateTime;
    /**
     * The failed attempts since the source hash last moved or a value
     * settled.
     */
    attempts: Generic.Int64;
    /** The last failure a deriver settled: { class?, message }. */
    failure: Generic.JSON;
    /**
     * Whether rederive asked for the field again while its hash had not
     * moved.
     */
    forced: boolean;
  };
}
