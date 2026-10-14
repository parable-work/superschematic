// The implementation of the fixture-paged-api API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.

import { notImplemented } from '@superschematic/http-runtime';
import type {
  Constructor,
  Deps,
  CatalogImplementation,
} from '@schemas/fixture-paged-api-api';

/** Builds the implementation of fixture-paged-api from its dependencies. Its type is the generated Constructor. */
export const create: Constructor = deps => ({
  catalog: catalogImplementation(deps),
});

function catalogImplementation(deps: Deps): CatalogImplementation {
  return {
    async placeBasket() {
      throw notImplemented('catalog.placeBasket');
    },
    async listItems() {
      throw notImplemented('catalog.listItems');
    },
    async getItem() {
      throw notImplemented('catalog.getItem');
    },
    async listTags() {
      throw notImplemented('catalog.listTags');
    },
  };
}
