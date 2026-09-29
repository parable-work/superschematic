import { EngineError } from './errors.js';

/** The page size of a list or an event read that names none. */
export const DEFAULT_PAGE_SIZE = 50;

/** The largest page a list or an event read returns. */
export const MAX_PAGE_SIZE = 500;

/** pageSize checks a requested page size and applies the default. */
export function pageSize(limit: number | undefined): number {
  if (limit === undefined) {
    return DEFAULT_PAGE_SIZE;
  }
  if (!Number.isInteger(limit) || limit < 1 || limit > MAX_PAGE_SIZE) {
    throw new EngineError('invalid_argument', `a page size is an integer from 1 to ${MAX_PAGE_SIZE}, got ${String(limit)}`);
  }
  return limit;
}
