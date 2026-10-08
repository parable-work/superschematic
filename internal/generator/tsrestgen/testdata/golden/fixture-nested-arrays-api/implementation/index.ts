// The implementation of the fixture-nested-arrays-api API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.

import { notImplemented } from '@superschematic/http-runtime';
import type {
  Constructor,
  Deps,
  GridImplementation,
} from '@schemas/fixture-nested-arrays-api-api';

/** Builds the implementation of fixture-nested-arrays-api from its dependencies. Its type is the generated Constructor. */
export const create: Constructor = deps => ({
  grid: gridImplementation(deps),
});

function gridImplementation(deps: Deps): GridImplementation {
  return {
    async saveGrid() {
      throw notImplemented('grid.saveGrid');
    },
    async getGrid() {
      throw notImplemented('grid.getGrid');
    },
    async gridLabels() {
      throw notImplemented('grid.gridLabels');
    },
    async replaceLabels() {
      throw notImplemented('grid.replaceLabels');
    },
  };
}
