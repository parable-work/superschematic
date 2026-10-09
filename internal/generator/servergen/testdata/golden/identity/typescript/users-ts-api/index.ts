// The implementation of the users-ts-api API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.

import { notImplemented } from '@superschematic/http-runtime';
import type {
  Constructor,
  Deps,
  GreetingImplementation,
} from '@schemas/users-ts-api-api';

/** Builds the implementation of users-ts-api from its dependencies. Its type is the generated Constructor. */
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
