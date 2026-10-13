import { HttpProblem, badRequest } from './problem.js';

/*
The page of a @paginated operation (D60). The generated router reads the
request's cursor and limit with readPage, the implementation takes the
PageRequest as args.page and returns a Page, {items, next}, which the
router answers as the envelope's data. A cursor is opaque to the caller:
the unpadded base64url of the compact JSON of the implementation's key,
the position of the last item of a page in the operation's order. The Go
runtime's paging package and the Rust runtime's page module read and build
the same, held to runtime/http/testdata/page_parity.json. Nothing here
imports node:, so it runs on Workers too.
*/

/** An operation's @paginated limits, as the router carries them. */
export interface PageSpec {
  readonly defaultLimit: number;
  readonly maxLimit: number;
}

/** The page a caller asks for: the cursor of the page before, absent for the first page, and the limit, its default filled in. */
export interface PageRequest {
  readonly cursor?: string;
  readonly limit: number;
}

/** A page: its items, which may be fewer than its limit, even none, and the cursor of the next page, null after the last. */
export interface Page<T> {
  readonly items: T[];
  readonly next: string | null;
}

/** The longest cursor a router reads, in characters. */
export const PAGE_CURSOR_MAX_LENGTH = 1024;

/** The detail and reason of a cursor the implementation cannot read (decodeCursor). */
export const REFUSED_CURSOR_DETAIL = 'cursor is not one this operation returned';
export const REFUSED_CURSOR_REASON = 'not a cursor this operation returned';

const CURSOR_PATTERN = /^[A-Za-z0-9_-]+$/;
const INTEGER_TEXT = /^[+-]?[0-9]+$/;
const ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_';

/** The query a router reads the page from: a URLSearchParams, or each key's values as Hono's queries() gives them. */
export type PageQuery = URLSearchParams | Readonly<Record<string, string | readonly string[] | undefined>>;

function firstValue(query: PageQuery, name: string): string | undefined {
  if (query instanceof URLSearchParams) return query.get(name) ?? undefined;
  const value = query[name];
  return typeof value === 'string' ? value : value?.[0];
}

/** The 400 of a parameter the request carries, worded as params.ts words a string or integer query parameter's, with the broken rule when there is one. */
function refuse(parameter: string, reason: string, validator?: string): never {
  throw badRequest(`Invalid query parameter ${parameter}: ${reason}`, {
    details: {
      location: 'query',
      parameter,
      reason,
      ...(validator !== undefined ? { errors: [{ validator, message: reason }] } : {}),
    },
  });
}

/**
 * Reads a page request from a query: the first value of cursor and of
 * limit, an empty one as absent. A cursor is 1 to 1024 characters of the
 * unpadded base64url alphabet; a limit a whole number from 1 to the spec's
 * maxLimit, its defaultLimit when absent. A refusal throws the 400
 * HttpProblem at the parameter, the cursor's first.
 */
export function readPage(query: PageQuery, spec: PageSpec): PageRequest {
  const cursor = firstValue(query, 'cursor');
  if (cursor) {
    if ([...cursor].length > PAGE_CURSOR_MAX_LENGTH) {
      refuse('cursor', `must be at most ${PAGE_CURSOR_MAX_LENGTH} characters`, 'maxLength');
    }
    if (!CURSOR_PATTERN.test(cursor)) refuse('cursor', 'does not match the required pattern', 'pattern');
  }
  let limit = spec.defaultLimit;
  const text = firstValue(query, 'limit');
  if (text) {
    if (!INTEGER_TEXT.test(text)) refuse('limit', 'expected an integer');
    limit = Number(text);
    if (!Number.isSafeInteger(limit)) refuse('limit', 'integer out of range');
    if (limit < 1) refuse('limit', 'must be at least 1', 'min');
    if (limit > spec.maxLimit) refuse('limit', `must be at most ${spec.maxLimit}`, 'max');
  }
  return cursor ? { cursor, limit } : { limit };
}

/**
 * The cursor of a position: the unpadded base64url of the compact JSON of
 * key. A key should encode short: a router reads a cursor of at most 1024
 * characters, about 768 bytes of JSON.
 */
export function encodeCursor(key: unknown): string {
  const text = JSON.stringify(key);
  if (text === undefined) throw new TypeError('encodeCursor: the key does not encode as JSON');
  const bytes = new TextEncoder().encode(text);
  let out = '';
  for (let i = 0; i < bytes.length; i += 3) {
    const n = (bytes[i]! << 16) | ((bytes[i + 1] ?? 0) << 8) | (bytes[i + 2] ?? 0);
    const chars = Math.min(bytes.length - i, 3) + 1;
    for (let j = 0; j < chars; j++) out += ALPHABET[(n >> (18 - 6 * j)) & 63];
  }
  return out;
}

/** The 400 of a cursor the implementation cannot read, which it may throw itself for one that decodes but names no position of its order. */
export function refusedCursor(): HttpProblem {
  return badRequest(REFUSED_CURSOR_DETAIL, {
    details: { location: 'query', parameter: 'cursor', reason: REFUSED_CURSOR_REASON },
  });
}

/** The bytes of a canonical unpadded base64url text, or null. */
function decodeBase64url(text: string): Uint8Array | null {
  if (text.length % 4 === 1) return null;
  const out = new Uint8Array(Math.floor((text.length * 3) / 4));
  let bits = 0;
  let count = 0;
  let at = 0;
  for (const char of text) {
    const value = ALPHABET.indexOf(char);
    if (value < 0) return null;
    bits = (bits << 6) | value;
    count += 6;
    if (count >= 8) {
      count -= 8;
      out[at++] = (bits >> count) & 0xff;
      bits &= (1 << count) - 1;
    }
  }
  // The bits past the last byte are zero in the canonical text.
  return bits === 0 ? out : null;
}

/**
 * Reads a cursor back into the implementation's key. A cursor that is not
 * of the alphabet, not the canonical unpadded base64url of UTF-8 JSON, is
 * refused with the 400 every runtime answers, at cursor: the caller starts
 * again. The key's JSON is not checked against K; an implementation that
 * finds no position in it throws refusedCursor() itself.
 */
export function decodeCursor<K>(cursor: string): K {
  if (cursor.length === 0 || cursor.length > PAGE_CURSOR_MAX_LENGTH || !CURSOR_PATTERN.test(cursor)) throw refusedCursor();
  const bytes = decodeBase64url(cursor);
  if (bytes === null) throw refusedCursor();
  let text: string;
  try {
    // A byte-order mark is kept, so JSON.parse refuses it as the Go and Rust decoders do.
    text = new TextDecoder('utf-8', { fatal: true, ignoreBOM: true }).decode(bytes);
  } catch {
    throw refusedCursor();
  }
  try {
    return JSON.parse(text) as K;
  } catch {
    throw refusedCursor();
  }
}

/** The last page: items and no next. */
export function lastPage<T>(items: T[]): Page<T> {
  return { items, next: null };
}

/** A page with a next page after it. */
export function morePage<T>(items: T[], next: string): Page<T> {
  return { items, next };
}

/**
 * The page of rows an implementation read for a request: it reads up to
 * limit + 1 rows in the operation's order, after the request's cursor, and
 * pageFromRows keeps limit of them and, when there were more, sets next to
 * the cursor of the last one kept, encodeCursor(key(row)). limit is the
 * request's; one below 1 is taken as 1.
 */
export function pageFromRows<T>(rows: readonly T[], limit: number, key: (row: T) => unknown): Page<T> {
  const keep = Math.max(limit, 1);
  if (rows.length <= keep) return lastPage([...rows]);
  const items = rows.slice(0, keep);
  return morePage(items, encodeCursor(key(items[keep - 1]!)));
}
