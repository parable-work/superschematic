// The implementation of the fixture-files-api API.
//
// superschematic wrote this package once, as a scaffold, because it was
// missing. It never writes it again: the package is yours. Each method
// answers 501 Not Implemented until you implement it.

import { notImplemented } from '@superschematic/http-runtime';
import type {
  Constructor,
  Deps,
  StatementsImplementation,
} from '@schemas/fixture-files-api-api';

/** Builds the implementation of fixture-files-api from its dependencies. Its type is the generated Constructor. */
export const create: Constructor = deps => ({
  statements: statementsImplementation(deps),
});

function statementsImplementation(deps: Deps): StatementsImplementation {
  return {
    async importStatement() {
      throw notImplemented('statements.importStatement');
    },
    async exportStatement() {
      throw notImplemented('statements.exportStatement');
    },
    async originalStatement() {
      throw notImplemented('statements.originalStatement');
    },
  };
}
