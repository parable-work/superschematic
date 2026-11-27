// Provenance's class (D103): the build derives provenance.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { Generic, Temporal } from "superscalar";
import { computed, declaresBehavior, subTable, type Const, type JsonSchema } from "@superschematic/db";

/** A state's name, as a config writes it. */
type StateName = JsonSchema<string, { pattern: "^[A-Za-z][A-Za-z0-9_-]*$"; maxLength: 64 }>;

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface ProvenanceConfig {
  /** The own top-level fields it records; every one when absent. */
  fields?: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  /** Which writes enter review, and who reviews them. */
  review?: {
    /**
     * The behaviors whose writes enter review, each one the type composes.
     */
    sources: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
    /**
     * A confidence at or above which a write is accepted with no reviewer;
     * every write from a source is proposed when absent.
     */
    below?: JsonSchema<number, { minimum: 0; maximum: 1 }>;
    /** The permission a reviewer needs to accept or reject. */
    permission: Named;
  };
  /**
   * By state of the type's Workflow, the fields a move into it requires out
   * of review.
   */
  gates?: JsonSchema<Record<StateName, {
    /** The recorded fields that must not be proposed, or all. */
    reviewed: JsonSchema<Named[], { minItems: 1; uniqueItems: true }> | Const<"all">;
  }>, { minProperties: 1 }>;
}

interface AcceptParams {
  fields: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
}

interface AcceptResult {
  accepted: string[];
  inReview: boolean;
}

interface RejectParams {
  fields: JsonSchema<Named[], { minItems: 1; uniqueItems: true }>;
  /**
   * Replacement values, by field of fields; a field it leaves out is cleared.
   */
  patch?: object;
}

interface RejectResult {
  rejected: string[];
  inReview: boolean;
}

/** The codes Provenance's refusals carry. */
enum ProvenanceVeto {
  /**
   * accept or reject of a field Provenance does not record, or a reject patch
   * that sets another field.
   */
  not_recorded,
  /** accept or reject of a field that is not proposed. */
  not_in_review,
  /**
   * A transition into a gated state while a field the gate lists is proposed.
   */
  unreviewed_fields,
}

/**
 * Who set each own field last, and through what (D73): a caller's create or
 * update, or the behavior whose operation wrote it, with the confidence that
 * operation attributed (D16, amended). With review, a write from the sources
 * it names is proposed until a reviewer holding its permission accepts or
 * rejects it, unless its confidence reaches below; a caller's write over a
 * proposal makes it edited. A gate refuses a move of the type's Workflow into
 * its state while a field it lists is proposed. Deleting the instance deletes
 * its records.
 */
@declaresBehavior({ vetoes: ProvenanceVeto })
export abstract class Provenance<C extends ProvenanceConfig> {
  /**
   * By recorded field written since Provenance was added: { source, by, for?,
   * at, confidence?, review? }. source is caller for a caller's create or
   * update, else the name of the behavior whose operation wrote it; by is the
   * principal's subject; for is the subject it acted for, when it acted for
   * one (D77); review is { state, by?, at? }, state proposed, accepted,
   * rejected or edited.
   */
  @computed fields: unknown;
  /** Whether any recorded field is proposed. A list filters on it. */
  inReview: boolean;

  /**
   * Accepts proposed fields as they are, as the caller. Needs the review's
   * permission.
   * @writes
   * @checks review.permission
   */
  abstract accept(params: AcceptParams): AcceptResult;

  /**
   * Rejects proposed fields: clears each, or sets it to the patch's value,
   * through update(), as the caller's own write, so Derived does not propose
   * it again. Needs the review's permission.
   * @writes
   * @checks review.permission
   */
  abstract reject(params: RejectParams): RejectResult;

  /**
   * On a table, one row per recorded field of the row, keyed by the field's
   * name: the last write's source, who made it and for whom, when, the
   * confidence its attribution gave, and its review. Every write of the row
   * keeps them in line; a delete of the row deletes them.
   */
  @subTable({ key: "field" })
  static records = class {
    /** The recorded field's name. */
    field: string;
    /**
     * caller for a caller's create or update, else the name of the behavior
     * whose operation wrote it.
     */
    source: string;
    /** The subject of the caller whose write it records. */
    bySubject: string;
    /**
     * The subject the caller acted for (D77); absent when it acted for no
     * one.
     */
    forSubject: string;
    /** When, in epoch milliseconds. */
    writtenAt: Temporal.DateTime;
    /** The confidence the write's attribution gave, from 0 to 1. */
    confidence: Generic.Probability;
    /**
     * proposed, accepted, rejected or edited; absent for a write that entered
     * no review.
     */
    review: string;
    /**
     * The subject of the reviewer, or of the caller whose write edited a
     * proposal.
     */
    reviewedBy: string;
    /** When the review settled, in epoch milliseconds. */
    reviewedAt: Temporal.DateTime;
  };
}
