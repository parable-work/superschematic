/*
Paging for a behavior's list operations, the core's and an extension's:
the engine's page sizes (a paramsSchema bounds limit to 1-500) and an
opaque cursor over a number the behavior gives each record: a comment's
id, a revision's number, a proposal's id, a row id. Each page is one
range read on that number, with a limit of limit + 1.
*/

import { OperationParamsError } from '../errors.js';
import { DEFAULT_PAGE_SIZE } from '../paging.js';
import type { FrozenJSON } from './behavior.js';

/** One page of a list operation's result. */
export interface Page<T> {
  readonly items: T[];
  /** The cursor of the next page; null after the last. */
  readonly next: string | null;
}

/**
 * pageRequest reads a list operation's limit and cursor: how many records
 * a page holds and the number the page starts after, 0 for the first.
 */
export function pageRequest(behavior: string, operation: string, params: FrozenJSON): { limit: number; after: number } {
  const limit = (params.limit as number | undefined) ?? DEFAULT_PAGE_SIZE;
  const cursor = params.cursor as string | undefined;
  if (cursor === undefined) {
    return { limit, after: 0 };
  }
  const match = /^after:([0-9]{1,15})$/.exec(Buffer.from(cursor, 'base64url').toString('utf8'));
  if (!match) {
    throw new OperationParamsError(behavior, operation, [{ path: '/cursor', message: 'is not a cursor this operation returned' }]);
  }
  return { limit, after: Number(match[1]) };
}

/**
 * page returns the first limit of rows, read with a limit of limit + 1,
 * and the cursor after the last of them when more follow.
 */
export function page<T>(rows: readonly T[], limit: number, numberOf: (item: T) => number): Page<T> {
  const items = rows.slice(0, limit);
  const next = rows.length > limit ? Buffer.from(`after:${numberOf(items[items.length - 1])}`, 'utf8').toString('base64url') : null;
  return { items, next };
}
