// The implementation of the fixture-user-routes-api API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.

import { notImplemented } from '@superschematic/http-runtime';
import type {
  Constructor,
  Deps,
  GreetingImplementation,
} from '@schemas/fixture-user-routes-api-api';

/** Builds the implementation of fixture-user-routes-api from its dependencies. Its type is the generated Constructor. */
export const create: Constructor = deps => ({
  greeting: greetingImplementation(deps),
});

function greetingImplementation(deps: Deps): GreetingImplementation {
  return {
    async greet() {
      throw notImplemented('greeting.greet');
    },
  };
}
