/*
Paging: the page sizes of a list and an event read, and the page a
behavior's list operation and the search across schemas return.
*/

/** The page size of a list or an event read that names none. */
export const DEFAULT_PAGE_SIZE = 50;

/** The largest page a list or an event read returns. */
export const MAX_PAGE_SIZE = 500;

/** One page of a list operation's result. */
export interface Page<T> {
  readonly items: T[];
  /** The cursor of the next page; null after the last. */
  readonly next: string | null;
}
