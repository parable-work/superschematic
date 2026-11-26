// Numbering's class (D103): the build derives numbering.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { Generic } from "superscalar";
import { declaresBehavior, subTable, valueField, type Integer, type JsonSchema } from "@superschematic/db";

interface NumberingConfig {
  /**
   * How a number prints: text with {seq}, or {seq:N} zero-padded to N digits
   * (1 to 10), exactly once, and {yyyy} or {yy}, the UTC year the instance
   * was created in, as often as wanted. No other {...} is allowed.
   */
  format: JsonSchema<string, { minLength: 1; maxLength: 128 }>;
  /**
   * Counts per target of a link of the type's Links config, which is required
   * and holds one target: each target's instances are numbered 1, 2, 3, ...
   * of their own.
   */
  scope?: {
    /** The link's name in the type's Links config. */
    link: JsonSchema<string, { pattern: "^[a-z][A-Za-z0-9]{0,63}$" }>;
  };
  /**
   * yearly starts the count again at 1 each UTC year, the year the instance
   * was created in; the format must then print the year, {yyyy} or {yy}.
   */
  reset?: "yearly";
}

type AssignNumberParams = JsonSchema<{}, { properties: {} }>;

interface AssignNumberResult {
  /** Its number in its count. */
  seq: JsonSchema<Integer, { minimum: 1 }>;
  /** The number as the format prints it. */
  label: string;
}

/**
 * A number a person reads on each instance, set in its create's transaction
 * from a count per namespace and schema: the format prints it,
 * INV-{yyyy}-{seq:4} as INV-2026-0042. The count may run per target of a
 * required, single-valued link and start again each UTC year, so a label is
 * unique within its scope. A refused create takes no number; a delete leaves
 * a gap. Nothing changes a number once set, and a list finds an instance by
 * its label or its number. An instance made before its schema composed
 * Numbering is numbered by assignNumber.
 */
@declaresBehavior()
export abstract class Numbering<C extends NumberingConfig> {
  /**
   * The instance's number in its count, from 1; absent until it is numbered.
   */
  seq: Generic.Int64;
  /**
   * The number as the format prints it, unique within its scope; absent until
   * it is numbered.
   */
  @valueField label?: string;

  /**
   * Numbers an instance created before its schema composed Numbering, in the
   * count of the year it was created in, as its create would have. On an
   * instance numbered already it changes nothing and returns its number. A
   * deployment backfills by invoking it in creation order.
   * @writes
   * @returns The instance's number.
   */
  abstract assignNumber(params: AssignNumberParams): AssignNumberResult;

  /**
   * On a table, one row per count, kept for the table as a whole: the last
   * number it gave and the config it counted under. A delete of a row leaves
   * it where it was, so a delete leaves a gap; a row whose config is not the
   * use's counts from 1 again.
   */
  @subTable({ key: "scope", shared: true })
  static counts = class {
    /**
     * What the count runs per: the key of the scope relation's target and the
     * UTC year, each the config counts by, joined with a slash, or * for a
     * count of the whole table.
     */
    scope: string;
    /** The last number the count gave. */
    last: Generic.Int64;
    /**
     * The format, the scope and the reset the count was made under, as JSON.
     */
    counting: string;
  };
}
