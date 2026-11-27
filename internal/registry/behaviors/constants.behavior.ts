// Constants's class (D103): the build derives constants.behavior.json from it
// (make behaviors), which the behaviors' core and the engine read.
import { declaresBehavior, type JsonSchema } from "@superschematic/db";

/** A string that is not empty. */
type Named = JsonSchema<string, { minLength: 1 }>;

interface ConstantsConfig {
  /**
   * The type's own top-level fields, by JSON key, that keep the value their
   * create gives them.
   */
  fields: JsonSchema<Named[], { minItems: 1; maxItems: 64; uniqueItems: true }>;
  /**
   * The permission a caller needs to change them after the create; nobody may
   * when absent.
   */
  permission?: Named;
  /**
   * Own fields, by JSON key, that move one way after the create: up, a
   * boolean that only turns true or a number or a date-time that only grows;
   * down, one that only turns false or shrinks. No value is below every
   * value, so clearing a field is a move down and giving a field with none a
   * value a move up. A move the other way is vetoed (one_way) unless the
   * caller holds the config's permission; a create gives any value. A field
   * is in fields or here, not both. A table's alone: an engine type refuses
   * it.
   */
  oneWay?: JsonSchema<Record<string, "up" | "down">, { minProperties: 1; maxProperties: 64 }>;
}

/** The codes Constants's refusals carry. */
enum ConstantsVeto {
  /**
   * An update that moves a oneWay field the other way, by a caller without
   * the config's permission; details give the field, its direction, its
   * current value and the one the update gives it, null for none. A table's
   * alone: an engine type refuses oneWay.
   */
  one_way,
}

/**
 * Fields of the type that its create sets and nothing changes after: an
 * update that changes one, a caller's or one a behavior's operation applies,
 * is refused at the field, unless the caller holds the config's permission. A
 * field the create leaves absent stays absent. What routes an instance, such
 * as a step's kind or the key a blueprint stamped it with, stays what its
 * create gave it.
 */
@declaresBehavior({ vetoes: ConstantsVeto })
export abstract class Constants<C extends ConstantsConfig> {
}
