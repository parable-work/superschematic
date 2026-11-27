// A behavior the project declares by its class (D103's step 5), whose
// hooks a Rust crate of the project implements over the core's API.
import { declaresBehavior, subTable, type Integer, type JsonSchema } from "@superschematic/db";

interface RatingConfig {
  /** The most stars a rating gives. */
  maxStars: JsonSchema<Integer, { minimum: 1; maximum: 10 }>;
  /** The permission a caller needs to clear the ratings. */
  clearPermission?: JsonSchema<string, { minLength: 1 }>;
}

interface RateParams {
  /** How many stars, from 1 to maxStars. */
  stars: JsonSchema<Integer, { minimum: 1 }>;
}

interface RateResult {
  /** How many ratings the row has. */
  count: Integer;
}

/** The codes Rating's refusals carry. */
enum RatingVeto {
  /** More stars than the config's maxStars. */
  too_many_stars,
}

/** Shoppers rate a row from one star to the most the config gives. */
@declaresBehavior({
  name: "notes.Rating",
  vetoes: RatingVeto,
  crate: { crate: "notes-rules", path: "crates/rules", register: "notes_rules::Rating" },
})
export abstract class Rating<T, C extends RatingConfig> {
  /** How many ratings the row has. */
  ratingCount: number;

  /**
   * Rates the row as the caller: a second rating replaces the first.
   * @writes
   */
  abstract rate(params: RateParams): RateResult;

  /**
   * Clears the row's ratings.
   * @writes
   * @checks clearPermission
   */
  abstract clear(): RateResult;

  /** One row per rater. */
  @subTable({ key: "rater" })
  static ratings = class {
    /** Who rated. */
    rater: string;
    /** How many stars. */
    stars: number;
  };
}
