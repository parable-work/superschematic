// The config of acme's behavior, for tsc. The acme extension declares
// acme.Rating (ext/rating.behavior.json) and the registry checks every
// use against that declaration; this augmentation of the core's
// BehaviorConfigs lets @behavior("acme.Rating", { maxStars: 5 }) type-check.
//
// A program sees the augmentation when it includes this file: every
// program that imports @acme/schema does, and a General service lists the
// file in its tsconfig.json, because a General schema cannot import
// @acme/schema (its decorators are for Catalog schemas).
import "@superschematic/schema";

/** acme.Rating's config: the highest rating a shopper can give, 3 to 10. */
export interface RatingConfig {
  readonly maxStars: number;
}

declare module "@superschematic/schema" {
  interface BehaviorConfigs {
    "acme.Rating": RatingConfig;
  }
}
