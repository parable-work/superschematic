// The implementation of the map-args-api API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.

import { notImplemented } from '@superschematic/http-runtime';
import type {
  Constructor,
  Deps,
  PostImplementation,
} from '@schemas/map-args-api-api';

/** Builds the implementation of map-args-api from its dependencies. Its type is the generated Constructor. */
export const create: Constructor = deps => ({
  post: postImplementation(deps),
});

function postImplementation(deps: Deps): PostImplementation {
  return {
    async nameThings() {
      throw notImplemented('post.nameThings');
    },
  };
}
