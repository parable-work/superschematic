// The implementation of the deps-ts-pricing API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.

import { notImplemented } from '@superschematic/http-runtime';
import type {
  Constructor,
  Deps,
  QuotesImplementation,
} from '@schemas/deps-ts-pricing-api';

/** Builds the implementation of deps-ts-pricing from its dependencies. Its type is the generated Constructor. */
export const create: Constructor = deps => ({
  quotes: quotesImplementation(deps),
});

function quotesImplementation(deps: Deps): QuotesImplementation {
  return {
    async getQuote() {
      throw notImplemented('quotes.getQuote');
    },
  };
}
