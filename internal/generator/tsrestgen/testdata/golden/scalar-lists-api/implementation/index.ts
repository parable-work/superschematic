// The implementation of the scalar-lists-api API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.

import { notImplemented } from '@superschematic/http-runtime';
import type {
  Constructor,
  Deps,
  TagImplementation,
} from '@schemas/scalar-lists-api-api';

/** Builds the implementation of scalar-lists-api from its dependencies. Its type is the generated Constructor. */
export const create: Constructor = deps => ({
  tag: tagImplementation(deps),
});

function tagImplementation(deps: Deps): TagImplementation {
  return {
    async storeDocument() {
      throw notImplemented('tag.storeDocument');
    },
    async findTags() {
      throw notImplemented('tag.findTags');
    },
    async saveTags() {
      throw notImplemented('tag.saveTags');
    },
  };
}
