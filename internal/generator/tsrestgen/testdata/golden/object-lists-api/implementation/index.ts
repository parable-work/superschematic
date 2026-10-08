// The implementation of the object-lists-api API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.

import { notImplemented } from '@superschematic/http-runtime';
import type {
  Constructor,
  Deps,
  DrawingImplementation,
} from '@schemas/object-lists-api-api';

/** Builds the implementation of object-lists-api from its dependencies. Its type is the generated Constructor. */
export const create: Constructor = deps => ({
  drawing: drawingImplementation(deps),
});

function drawingImplementation(deps: Deps): DrawingImplementation {
  return {
    async saveOutline() {
      throw notImplemented('drawing.saveOutline');
    },
  };
}
