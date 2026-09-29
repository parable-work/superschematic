/*
acme.Rating for @superschematic/engine: shoppers rate a catalog item from
one star to the maximum its type configures (D16). The declaration is the
copy `acme-schematic behaviors --extension acme` writes from
ext/rating.behavior.json, the file the compiler registers, so the engine
and the compiler read one declaration.

The behavior keeps two columns on each instance, the number of ratings
and their sum, and reads its declared fields from them. rate adds one
rating; ratingSummary reads them. A new version may raise maxStars, which
every stored rating still fits, and may not lower it.
*/

import { EngineError, defineBehavior } from '@superschematic/engine';

import declaration from '../declarations/acme.Rating.behavior.json' with { type: 'json' };

/** A type's config of acme.Rating, as ext/rating.behavior.json declares it. */
export interface RatingConfig {
  readonly maxStars: number;
}

interface Totals {
  count: number;
  stars: number;
}

export const rating = defineBehavior<RatingConfig>({
  declaration,
  configChange(before, after) {
    if (before === undefined || after === undefined) {
      return 'the ratings an item has would be lost or start from nothing';
    }
    return after.maxStars >= before.maxStars ? undefined : `maxStars cannot fall from ${before.maxStars} to ${after.maxStars}`;
  },
  migrations: [
    {
      version: 1,
      name: 'rating totals',
      columns: {
        count: { type: 'integer', notNull: true, default: 0 },
        stars: { type: 'integer', notNull: true, default: 0 },
      },
    },
  ],
  operations: {
    rate(context, params) {
      const stars = params.stars as number;
      if (stars > context.config.maxStars) {
        throw new EngineError('invalid_argument', `a rating of ${context.schema} is 1 to ${context.config.maxStars} stars, not ${stars}`);
      }
      const before = totals(context.columns.get());
      const after = { count: before.count + 1, stars: before.stars + stars };
      context.columns.set(after);
      return summary(after);
    },
    ratingSummary(context) {
      return summary(totals(context.columns.get()));
    },
  },
  fields: {
    ratingCount: (view) => summary(totals(view.columns.get())).ratingCount,
    ratingAverage: (view) => summary(totals(view.columns.get())).ratingAverage,
  },
});

function totals(columns: Record<string, unknown>): Totals {
  return { count: Number(columns.count), stars: Number(columns.stars) };
}

function summary({ count, stars }: Totals): { ratingCount: number; ratingAverage: number } {
  return { ratingCount: count, ratingAverage: count === 0 ? 0 : stars / count };
}
