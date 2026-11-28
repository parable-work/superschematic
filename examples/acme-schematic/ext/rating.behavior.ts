// acme.Rating's class (D103's step 5): the build derives
// rating.behavior.json from it, the declaration this package embeds and
// registers. It declares no crate:
// its rules are the declarative vocabulary's, which the behaviors' core
// interprets, so every host runs it from the declaration alone.
import { Generic, Temporal } from "superscalar";
import { declaresBehavior, subTable, type Integer, type JsonSchema, type Open } from "@superschematic/db";

interface RatingConfig {
  /** The highest rating a shopper can give. */
  maxStars: JsonSchema<Integer, { minimum: 3; maximum: 10 }>;
}

interface RateParams {
  stars: JsonSchema<Integer, { minimum: 1 }>;
}

interface RatingSummary {
  ratingCount: Integer;
  ratingAverage: number;
}

/**
 * Shoppers rate a catalog item from one star to the configured maximum,
 * one rating per shopper.
 */
@declaresBehavior({
  name: "acme.Rating",
  rules: {
    operations: {
      rate: {
        params: { stars: { lte: { config: "maxStars" } } },
        effects: [
          {
            upsert: {
              table: "ratings",
              key: { caller: true },
              values: { stars: { param: "stars" }, ratedAt: { now: true } },
            },
          },
        ],
        result: { ratingCount: { field: "ratingCount" }, ratingAverage: { field: "ratingAverage" } },
      },
      ratingSummary: {
        result: { ratingCount: { field: "ratingCount" }, ratingAverage: { field: "ratingAverage" } },
      },
    },
    derived: {
      ratingCount: { count: { table: "ratings" } },
      ratingAverage: { average: { table: "ratings", field: "stars", empty: 0 } },
    },
    configChange: { oneWay: { maxStars: "up" } },
  },
})
export abstract class Rating<C extends RatingConfig> {
  /** How many ratings the item has. */
  ratingCount: Generic.Int64;
  /** The mean rating; 0 before the first. */
  ratingAverage: number;

  /**
   * Records one shopper's rating of the item, replacing the shopper's
   * earlier one.
   * @writes
   * @invocation always
   */
  abstract rate(params: RateParams): Open<RatingSummary>;

  /** Returns the item's rating count and average. */
  abstract ratingSummary(): Open<RatingSummary>;

  /**
   * One row per shopper who rated the item: the stars and when the rating
   * came.
   */
  @subTable({ key: "rater" })
  static ratings = class {
    /** The shopper's subject. */
    rater: string;
    /** The stars the shopper gave, 1 to maxStars. */
    stars: Generic.Int64;
    /** When the shopper rated the item last. */
    ratedAt: Temporal.DateTime;
  };
}
